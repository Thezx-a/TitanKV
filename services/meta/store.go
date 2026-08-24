// Package meta 是 TitanKV 的元数据服务 (Phase 4).
//
// 职责:
//   - Collection 元数据 CRUD (name / type / ttl / schema / rag_config / created_at / updated_at)
//   - etcd watch 实现配置热更新 (所有 Meta 实例实时收到变更通知)
//
// 为什么用 etcd 而不是 Redis:
//   - etcd 基于 Raft 强一致, 元数据丢失会导致业务异常
//   - Redis 主从异步复制, 故障切换可能丢数据
//   - 限流/session 这种 "可丢可重建" 数据用 Redis 即可
//
// 启动: go run ./services/meta
// 端口: 8083 (默认)
package meta

import (
	"errors"
	"sync"
	"time"
)

// CollectionType 区分普通 KV 命名空间与 RAG 知识库.
const (
	CollectionTypeKV  = "kv"
	CollectionTypeRAG = "rag"
)

// RagCollectionConfig 知识库配置 (仅 Type == "rag" 时有效).
// 与 services/rag/store.go 中的 RagCollectionConfig 字段保持一致,
// 由 meta 持久化后写入 minikv 的 rag:col:{name}:cfg key.
type RagCollectionConfig struct {
	EmbeddingModel   string   `json:"embedding_model"`    // e.g. "bge-small-zh-v1.5"
	EmbeddingDim     int      `json:"embedding_dim"`     // e.g. 512
	DistanceMetric   string   `json:"distance_metric"`    // "cosine"|"dot"|"l2"
	ChunkSize        int      `json:"chunk_size"`         // tokens
	ChunkOverlap     int      `json:"chunk_overlap"`
	TopKDefault      int      `json:"top_k_default"`
	RerankEnabled    bool     `json:"rerank_enabled"`
	AllowedFileTypes []string `json:"allowed_file_types"`
	MaxDocSizeMB     int      `json:"max_doc_size_mb"`
	Owner            string   `json:"owner"`
	Visibility       string   `json:"visibility"` // "private"|"shared"
}

// Collection 命名空间元数据.
type Collection struct {
	Name      string              `json:"name"`
	Type      string              `json:"type"` // "kv" (默认) | "rag"
	TTL       int                 `json:"ttl_seconds"`
	Schema    map[string]string   `json:"schema"`
	RagConfig *RagCollectionConfig `json:"rag_config,omitempty"`
	CreatedAt int64               `json:"created_at"`
	UpdatedAt int64               `json:"updated_at"`
}

var (
	ErrCollectionNotFound       = errors.New("collection not found")
	ErrCollectionAlreadyExists  = errors.New("collection already exists")
	ErrRAGConfigRequired        = errors.New("rag_config required when type=rag")
	ErrRAGConfigNotApplicable   = errors.New("rag_config not allowed when type!=rag")
)

// Store 元数据存储. 内存版 + 可选 etcd watch 同步.
type Store struct {
	mu   sync.RWMutex
	data map[string]*Collection
}

func NewStore() *Store {
	return &Store{data: make(map[string]*Collection)}
}

// Create 创建 Collection.
func (s *Store) Create(c *Collection) error {
	if err := validateCollection(c); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[c.Name]; ok {
		return ErrCollectionAlreadyExists
	}
	now := time.Now().Unix()
	c.CreatedAt = now
	c.UpdatedAt = now
	cp := *c
	s.data[c.Name] = &cp
	return nil
}

// Find 查找.
func (s *Store) Find(name string) (*Collection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.data[name]
	if !ok {
		return nil, ErrCollectionNotFound
	}
	cp := *c
	return &cp, nil
}

// List 列出所有.
func (s *Store) List() []*Collection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Collection, 0, len(s.data))
	for _, c := range s.data {
		cp := *c
		out = append(out, &cp)
	}
	return out
}

// Update 更新 (TTL/Schema/RagConfig). type 创建后不可变.
func (s *Store) Update(name string, ttl int, schema map[string]string, rag *RagCollectionConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data[name]
	if !ok {
		return ErrCollectionNotFound
	}
	c.TTL = ttl
	c.Schema = schema
	if c.Type == CollectionTypeRAG {
		c.RagConfig = rag
	} else if rag != nil {
		return ErrRAGConfigNotApplicable
	}
	c.UpdatedAt = time.Now().Unix()
	return nil
}

// Delete 删除.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[name]; !ok {
		return ErrCollectionNotFound
	}
	delete(s.data, name)
	return nil
}

// ApplyWatchEvent 应用 etcd watch 事件到本地缓存.
// 用于多个 Meta 实例间同步.
func (s *Store) ApplyWatchEvent(typ string, c *Collection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch typ {
	case "PUT":
		if c == nil {
			return
		}
		s.data[c.Name] = c
	case "DELETE":
		if c != nil {
			delete(s.data, c.Name)
		}
	}
}

// validateCollection 校验 type 与 rag_config 的一致性.
func validateCollection(c *Collection) error {
	if c.Type == "" {
		c.Type = CollectionTypeKV
	}
	switch c.Type {
	case CollectionTypeKV:
		if c.RagConfig != nil {
			return ErrRAGConfigNotApplicable
		}
	case CollectionTypeRAG:
		if c.RagConfig == nil {
			return ErrRAGConfigRequired
		}
	default:
		return errors.New("invalid collection type: " + c.Type)
	}
	return nil
}
