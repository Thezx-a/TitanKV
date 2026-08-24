package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
)

// Service 是 RAG 服务的对外入口, 聚合 store / index / embedder / ingester / retriever / chat.
type Service struct {
	cfg       Config
	store     *Store
	index     VectorIndex
	embedder  Embedder
	ingester  *Ingester
	retriever *Retriever
	chat      *ChatOrchestrator
}

// NewService 构造 Service (由 cmd/main.go 调用).
func NewService(cfg Config) (*Service, error) {
	store := NewStore(cfg.MinikvAddr)

	// embedder: local (hash) | openai
	var emb Embedder
	switch cfg.EmbeddingProvider {
	case "openai":
		emb = NewOpenAIEmbedder(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDim)
	default:
		emb = NewHashEmbedder(cfg.EmbeddingDim)
	}
	emb = NewCachedEmbedder(emb, store)

	idx := NewVectorIndex(emb.Dim(), cfg.IndexType)
	loadAllSnapshots(cfg.IndexDir, idx)

	chunker := NewChunker(512, 64)
	ing := NewIngester(store, chunker, emb, idx, cfg)
	ret := NewRetriever(emb, idx, store, NewReranker(cfg.EnableRerank), cfg.DefaultTopK)

	var cp ChatProvider
	switch cfg.ChatProvider {
	case "openai":
		cp = NewOpenAIChatProvider(cfg.ChatBaseURL, cfg.ChatAPIKey, cfg.ChatModel)
	default:
		cp = NewMockChatProvider()
	}
	chatOrch := NewChatOrchestrator(ret, cp, store, cfg.DefaultTopK)

	return &Service{
		cfg: cfg, store: store, index: idx, embedder: emb,
		ingester: ing, retriever: ret, chat: chatOrch,
	}, nil
}

// Close 释放底层资源.
func (s *Service) Close() error { return s.store.Close() }

// RegisterRoutes 注册路由 (被 cmd/main.go 调用).
//
//	GET    /healthz
//	POST   /api/rag/collections/:col/documents   入库 (multipart file | json text)
//	GET    /api/rag/collections/:col/documents   列表
//	GET    /api/rag/collections/:col/documents/:doc  详情 (含 chunks)
//	DELETE /api/rag/collections/:col/documents/:doc  删除
//	POST   /api/rag/collections/:col/retrieve      检索
//	POST   /api/rag/collections/:col/chat          流式问答 (SSE)
//	GET    /api/rag/tasks/:task_id                任务状态
//	POST   /api/rag/collections/:col/eval            评测 Recall@K/MRR
func (s *Service) RegisterRoutes(r *gin.Engine) {
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":      "ok",
			"service":     "rag",
			"backend":     s.store.Backend(),
			"embedding":   fmt.Sprintf("%s dim=%d", s.cfg.EmbeddingProvider, s.embedder.Dim()),
			"chat":        s.cfg.ChatProvider,
			"index_size":  s.index.Size(),
			"index_dir":   s.cfg.IndexDir,
		})
	})

	r.POST("/api/rag/collections/:col/documents", s.IngestDocument)
	r.GET("/api/rag/collections/:col/documents", s.ListDocuments)
	r.GET("/api/rag/collections/:col/documents/:doc", s.GetDocument)
	r.DELETE("/api/rag/collections/:col/documents/:doc", s.DeleteDocument)
	r.POST("/api/rag/collections/:col/retrieve", s.Retrieve)
	r.POST("/api/rag/collections/:col/chat", s.Chat)
	r.GET("/api/rag/tasks/:task_id", s.GetTask)
	r.POST("/api/rag/tasks/:task_id/resume", s.ResumeTask)
	r.POST("/api/rag/collections/:col/eval", s.EvalCollection)
	r.GET("/api/rag/index/snapshot", s.SaveSnapshot)
}

// ---- 入库 ----

func (s *Service) IngestDocument(c *gin.Context) {
	col := c.Param("col")
	if err := ensureColName(col); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var text, title, source string
	if file, fh, err := c.Request.FormFile("file"); err == nil {
		defer file.Close()
		b, _ := io.ReadAll(file)
		text = string(b)
		title = c.PostForm("title")
		if title == "" {
			title = fh.Filename
		}
		source = fh.Filename
	} else {
		var req struct {
			Title string `json:"title"`
			Text  string `json:"text" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		text = req.Text
		title = req.Title
		source = "inline"
	}

	if len(text) > s.cfg.MaxDocSizeMB*1024*1024 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("doc exceeds %dMB", s.cfg.MaxDocSizeMB)})
		return
	}

	task, err := s.ingester.Ingest(c.Request.Context(), col, "", title, source, text)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "task": task})
		return
	}
	c.JSON(http.StatusOK, task)
}

// ---- 文档列表 / 详情 / 删除 ----

func (s *Service) ListDocuments(c *gin.Context) {
	col := c.Param("col")
	docs, err := s.store.ListDocuments(col)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": docs, "count": len(docs)})
}

func (s *Service) GetDocument(c *gin.Context) {
	col := c.Param("col")
	doc := c.Param("doc")
	var meta DocumentMeta
	ok, err := s.store.GetJSON(docMetaKey(col, doc), &meta)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
		return
	}
	chunks, _ := s.store.ListChunks(col, doc)
	c.JSON(http.StatusOK, gin.H{"meta": meta, "chunks": chunks})
}

func (s *Service) DeleteDocument(c *gin.Context) {
	col := c.Param("col")
	doc := c.Param("doc")
	if err := s.store.DeleteDocument(col, doc); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	s.index.ClearByPrefix(col + "/" + doc + "/")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- 检索 ----

func (s *Service) Retrieve(c *gin.Context) {
	col := c.Param("col")
	var req struct {
		Query string `json:"query" binding:"required"`
		TopK  int    `json:"top_k"`
		Debug bool   `json:"debug"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	start := time.Now()
	hits, err := s.retriever.Retrieve(c.Request.Context(), col, req.Query, req.TopK)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"hits":       hits,
		"count":      len(hits),
		"latency_ms": time.Since(start).Milliseconds(),
	})
}

// ---- 流式问答 (SSE) ----
//
// 事件流:
//
//	event: token     data: {"text":"当"}
//	event: citation  data: {"doc_id":"..."}
//	event: end        data: {"tokens":N,"latency_ms":M}
//	event: error      data: {"msg":"..."}
func (s *Service) Chat(c *gin.Context) {
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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	answer, citations, err := s.chat.Ask(ctx, col, req.Query, uid, sid, req.TopK, func(tok string) error {
		send("token", map[string]string{"text": tok})
		return nil
	})
	if err != nil {
		send("error", map[string]string{"msg": err.Error()})
		return
	}
	for _, cid := range citations {
		send("citation", map[string]string{"doc_id": cid})
	}
	send("end", map[string]any{"tokens": len(answer), "latency_ms": time.Since(start).Milliseconds()})
}

// ---- 任务状态 ----

func (s *Service) GetTask(c *gin.Context) {
	taskID := c.Param("task_id")
	t, err := s.store.LoadTask(taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, t)
}

func (s *Service) ResumeTask(c *gin.Context) {
	taskID := c.Param("task_id")
	t, err := s.ingester.ResumeIngest(c.Request.Context(), taskID)
	if err != nil && t == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusAccepted, gin.H{"task": t, "warning": err.Error()})
		return
	}
	c.JSON(http.StatusOK, t)
}

// ---- 评测 ----

func (s *Service) EvalCollection(c *gin.Context) {
	col := c.Param("col")
	var body struct {
		Queries []EvalQuery `json:"queries"`
		TopK    int         `json:"top_k"`
	}
	if err := c.BindJSON(&body); err != nil || len(body.Queries) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "queries required"})
		return
	}
	// Convert to eval types with JSON tags
	eqs := make([]EvalQuery, len(body.Queries))
	for i, q := range body.Queries {
		eqs[i] = EvalQuery{Query: q.Query, RelevantIDs: q.RelevantIDs}
	}
	res := Evaluate(c.Request.Context(), s.retriever, col, eqs, body.TopK)
	c.JSON(http.StatusOK, res)
}

// ---- 索引快照 ----

func (s *Service) SaveSnapshot(c *gin.Context) {
	path := filepath.Join(s.cfg.IndexDir, fmt.Sprintf("all-%d.idx", time.Now().Unix()))
	snap, ok := s.index.(SnapshotStore)
	if !ok {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "index does not support snapshots"})
		return
	}
	if err := snap.SaveSnapshot(path); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "path": path, "size": s.index.Size()})
}

// loadAllSnapshots 启动时从 IndexDir 加载所有 *.idx 合并进索引 (重建内存索引).
func loadAllSnapshots(dir string, idx VectorIndex) {
	if dir == "" {
		return
	}
	snap, ok := idx.(SnapshotStore)
	if !ok {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.idx"))
	for _, m := range matches {
		_ = snap.MergeSnapshot(m)
	}
}
