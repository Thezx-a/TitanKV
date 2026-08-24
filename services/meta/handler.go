package meta

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Service 是 Meta 服务.
type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

// CollectionRequest 创建/更新 Collection 请求.
//
//   - Type: "kv" (默认) | "rag"
//   - RagConfig: 仅当 Type=="rag" 时必填
//   - TTL / Schema: 普通 KV 命名空间参数
type CollectionRequest struct {
	Name      string              `json:"name" binding:"required"`
	Type      string              `json:"type"`
	TTL       int                 `json:"ttl_seconds"`
	Schema    map[string]string   `json:"schema"`
	RagConfig *RagCollectionConfig `json:"rag_config,omitempty"`
}

// CreateCollection POST /api/meta/collections
func (s *Service) CreateCollection(c *gin.Context) {
	var req CollectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	col := &Collection{
		Name:      req.Name,
		Type:      req.Type,
		TTL:       req.TTL,
		Schema:    req.Schema,
		RagConfig: req.RagConfig,
	}
	if err := s.store.Create(col); err != nil {
		status := http.StatusConflict
		if err == ErrRAGConfigRequired || err == ErrRAGConfigNotApplicable {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, col)
}

// GetCollection GET /api/meta/collections/:name
func (s *Service) GetCollection(c *gin.Context) {
	name := c.Param("name")
	col, err := s.store.Find(name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, col)
}

// ListCollections GET /api/meta/collections
func (s *Service) ListCollections(c *gin.Context) {
	cols := s.store.List()
	c.JSON(http.StatusOK, gin.H{"items": cols, "count": len(cols)})
}

// UpdateCollection PUT /api/meta/collections/:name
func (s *Service) UpdateCollection(c *gin.Context) {
	name := c.Param("name")
	var req CollectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.store.Update(name, req.TTL, req.Schema, req.RagConfig); err != nil {
		status := http.StatusNotFound
		if err == ErrRAGConfigNotApplicable {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	col, _ := s.store.Find(name)
	c.JSON(http.StatusOK, col)
}

// DeleteCollection DELETE /api/meta/collections/:name
func (s *Service) DeleteCollection(c *gin.Context) {
	name := c.Param("name")
	if err := s.store.Delete(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RegisterRoutes 注册路由.
func (s *Service) RegisterRoutes(r *gin.Engine) {
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "meta"})
	})
	r.POST("/api/meta/collections", s.CreateCollection)
	r.GET("/api/meta/collections", s.ListCollections)
	r.GET("/api/meta/collections/:name", s.GetCollection)
	r.PUT("/api/meta/collections/:name", s.UpdateCollection)
	r.DELETE("/api/meta/collections/:name", s.DeleteCollection)
}
