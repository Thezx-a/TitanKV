package rag

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// registerWikiRoutes mounts TitanWiki HTTP API on the existing gin engine.
func (s *Service) registerWikiRoutes(r *gin.Engine) {
	if !s.cfg.EnableWiki || s.wiki == nil {
		return
	}
	r.POST("/api/rag/collections/:col/wiki/compile", s.WikiCompile)
	r.GET("/api/rag/collections/:col/wiki/tasks/:id", s.WikiGetTask)
	r.GET("/api/rag/collections/:col/wiki/pages/:slug", s.WikiGetPage)
	r.GET("/api/rag/collections/:col/wiki/index", s.WikiGetIndex)
	r.GET("/api/rag/collections/:col/wiki/graph", s.WikiGetGraph)
	r.GET("/api/rag/collections/:col/wiki/contested", s.WikiListContested)
	r.POST("/api/rag/collections/:col/wiki/ask", s.WikiAsk)
}

// WikiCompile enqueues async compile for one doc or all docs in the collection.
func (s *Service) WikiCompile(c *gin.Context) {
	col := c.Param("col")
	if err := ensureColName(col); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var req struct {
		DocID string `json:"doc_id"`
	}
	_ = c.ShouldBindJSON(&req)

	if s.compilePool == nil || s.compiler == nil {
		if req.DocID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "doc_id required"})
			return
		}
		task, err := s.compiler.CompileDocument(c.Request.Context(), col, req.DocID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "task": task})
			return
		}
		c.JSON(http.StatusOK, task)
		return
	}

	if req.DocID == "" {
		docs, err := s.store.ListDocuments(col)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		ids := make([]string, 0, len(docs))
		for _, d := range docs {
			id, err := s.compilePool.Enqueue(col, d.DocID)
			if err != nil {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error(), "queued": ids})
				return
			}
			ids = append(ids, id)
		}
		c.JSON(http.StatusAccepted, gin.H{"task_ids": ids, "count": len(ids)})
		return
	}
	taskID, err := s.compilePool.Enqueue(col, req.DocID)
	if err != nil {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error(), "task_id": taskID})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"task_id": taskID, "status": TaskPending})
}

func (s *Service) WikiGetTask(c *gin.Context) {
	t, err := s.wiki.LoadCompileTask(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, t)
}

func (s *Service) WikiGetPage(c *gin.Context) {
	p, err := s.wiki.GetPage(c.Param("col"), c.Param("slug"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if p == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "page not found"})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Service) WikiGetIndex(c *gin.Context) {
	idx, err := s.wiki.LoadIndex(c.Param("col"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, idx)
}

func (s *Service) WikiGetGraph(c *gin.Context) {
	col := c.Param("col")
	slug := c.Query("slug")
	depth, _ := strconv.Atoi(c.DefaultQuery("depth", "1"))
	if slug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slug required"})
		return
	}
	g, err := BuildWikiGraph(s.wiki, col, slug, depth)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, g)
}

func (s *Service) WikiListContested(c *gin.Context) {
	pages, err := s.wiki.ListContested(c.Param("col"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": pages, "count": len(pages)})
}

// WikiAsk is SSE Q&A with wiki-first retrieval.
func (s *Service) WikiAsk(c *gin.Context) {
	col := c.Param("col")
	var req struct {
		Query     string `json:"query" binding:"required"`
		SessionID string `json:"session_id"`
		TopK      int    `json:"top_k"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	uid := c.GetHeader("X-User-ID")
	sid := req.SessionID
	if sid == "" {
		sid = "default"
	}
	topK := req.TopK
	if topK <= 0 {
		topK = s.cfg.DefaultTopK
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}
	send := func(event string, data any) {
		buf, _ := json.Marshal(data)
		fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, buf)
		flusher.Flush()
	}

	start := time.Now()
	answer, citations, err := s.chat.AskWiki(c.Request.Context(), col, req.Query, uid, sid, topK, s.wikiQ, func(tok string) error {
		send("token", map[string]string{"text": tok})
		return nil
	})
	if err != nil {
		send("error", map[string]string{"msg": err.Error()})
		return
	}
	for _, cid := range citations {
		if strings.HasPrefix(cid, "wiki:") {
			send("citation", map[string]string{"slug": strings.TrimPrefix(cid, "wiki:")})
		} else {
			send("citation", map[string]string{"doc_id": cid})
		}
	}
	send("end", map[string]any{"tokens": len([]rune(answer)), "latency_ms": time.Since(start).Milliseconds()})
}
