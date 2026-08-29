package data

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Service is the Data service.
type Service struct {
	store KVStore
}

// NewService creates the data service.
func NewService(store KVStore) *Service {
	return &Service{store: store}
}

// PutRequest is the POST /api/data/kv body.
type PutRequest struct {
	Key   string `json:"key" binding:"required"`
	Value string `json:"value" binding:"required"`
}

// Put POST /api/data/kv — write a KV pair.
func (s *Service) Put(c *gin.Context) {
	var req PutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.store.Put(req.Key, req.Value); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "backend": s.store.Backend()})
}

// Get GET /api/data/kv?key=xxx
func (s *Service) Get(c *gin.Context) {
	key := c.Query("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing key"})
		return
	}
	v, ok, err := s.store.Get(key)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	c.JSON(http.StatusOK, KVPair{Key: key, Value: v})
}

// Delete DELETE /api/data/kv?key=xxx (idempotent).
func (s *Service) Delete(c *gin.Context) {
	key := c.Query("key")
	if key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing key"})
		return
	}
	if err := s.store.Delete(key); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Scan GET /api/data/scan?start=xxx&end=xxx — SSE stream.
func (s *Service) Scan(c *gin.Context) {
	start := c.Query("start")
	end := c.Query("end")

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}

	items, err := s.store.Scan(start, end)
	if err != nil {
		fmt.Fprintf(c.Writer, "event: error\ndata: %q\n\n", err.Error())
		flusher.Flush()
		return
	}
	count := 0
	for _, kv := range items {
		buf, err := json.Marshal(kv)
		if err != nil {
			continue
		}
		fmt.Fprintf(c.Writer, "data: %s\n\n", buf)
		flusher.Flush()
		count++
	}
	endPayload, _ := json.Marshal(map[string]int{"count": count})
	fmt.Fprintf(c.Writer, "event: end\ndata: %s\n\n", endPayload)
	flusher.Flush()
}

// RegisterRoutes registers HTTP routes.
func (s *Service) RegisterRoutes(r *gin.Engine) {
	r.GET("/healthz", func(c *gin.Context) {
		status := "ok"
		kv := "ok"
		if _, _, err := s.store.Get("__titankv_health_probe__"); err != nil {
			status = "degraded"
			kv = "down"
		}
		c.JSON(http.StatusOK, gin.H{
			"status":  status,
			"service": "data",
			"backend": s.store.Backend(),
			"kv":      kv,
			"keys":    s.store.Size(),
		})
	})
	r.POST("/api/data/kv", s.Put)
	r.GET("/api/data/kv", s.Get)
	r.DELETE("/api/data/kv", s.Delete)
	r.GET("/api/data/scan", s.Scan)
}
