package rag

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/titan-kv/titan/services/data"
)

// Store 封装对 minikv_server 的访问, 复用 services/data 的 TCP 客户端.
//
// 按 RagKv.md §4 的前缀化 key 空间组织数据, 利用 minikv Scan 做前缀遍历:
//
//	rag:col:{col}:cfg                  → RagCollectionConfig (这里不存, 由 meta 服务管)
//	rag:doc:{col}:{doc}:meta           → DocumentMeta
//	rag:doc:{col}:{doc}:status         → "pending"|"running"|"success"|"failed"
//	rag:chunk:{col}:{doc}:{seq:08d}    → ChunkRecord (正文)
//	rag:task:{task_id}                 → IngestTask
//	rag:cache:emb:{hash}               → 向量缓存
//	rag:chat:{uid}:{sid}:{seq:08d}      → ChatMessage
//	rag:qlog:{col}:{req_id}            → QueryLog
// kvClient is the minikv-facing surface used by Store (real TCP or MemKV in tests).
type kvClient interface {
	Put(key, value string) error
	Get(key string) (string, bool, error)
	Delete(key string) error
	DeleteRange(start, end string) error
	Scan(start, end string) ([]data.KVPair, error)
	WriteBatch(ops []data.BatchOp) error
	Close() error
}

type Store struct {
	kv      kvClient
	backend string
}

// NewStore 创建一个直连 minikv_server 的 RAG 存储.
// addr 形如 "127.0.0.1:8888", 来自 Config.MinikvAddr.
func NewStore(addr string) *Store {
	return &Store{kv: data.NewMiniKVClient(addr), backend: "minikv"}
}

// NewStoreFromKV wraps an arbitrary kvClient (e.g. MemKV for unit tests).
func NewStoreFromKV(kv kvClient) *Store {
	return &Store{kv: kv, backend: "mem"}
}

// Close 释放底层 TCP 连接.
func (s *Store) Close() error { return s.kv.Close() }

// Backend 返回存储后端标识 (用于 healthz).
func (s *Store) Backend() string {
	if s.backend != "" {
		return s.backend
	}
	return "minikv"
}

// ---- 基础 KV 操作 (透传到 data.MiniKVClient) ----

func (s *Store) Put(key, value string) error { return s.kv.Put(key, value) }

func (s *Store) Get(key string) (string, bool, error) { return s.kv.Get(key) }

func (s *Store) Delete(key string) error { return s.kv.Delete(key) }

// DeleteRange removes all keys in [start, end).
func (s *Store) DeleteRange(start, end string) error { return s.kv.DeleteRange(start, end) }

// Scan 前缀区间扫描 [start, end).
func (s *Store) Scan(start, end string) ([]data.KVPair, error) { return s.kv.Scan(start, end) }

// WriteBatch atomically applies Put/Del ops.
func (s *Store) WriteBatch(ops []data.BatchOp) error { return s.kv.WriteBatch(ops) }

// ---- JSON 读写便捷方法 ----

func (s *Store) PutJSON(key string, v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.kv.Put(key, string(buf))
}

func (s *Store) GetJSON(key string, out any) (bool, error) {
	raw, ok, err := s.kv.Get(key)
	if err != nil || !ok {
		return ok, err
	}
	return true, json.Unmarshal([]byte(raw), out)
}

// ---- key 空间编码 ----

func chunkKey(col, docID string, seq int) string {
	return fmt.Sprintf("rag:chunk:%s:%s:%08d", col, docID, seq)
}

func chunkPrefix(col, docID string) string {
	return fmt.Sprintf("rag:chunk:%s:%s:", col, docID)
}

func docMetaKey(col, docID string) string {
	return fmt.Sprintf("rag:doc:%s:%s:meta", col, docID)
}

func docStatusKey(col, docID string) string {
	return fmt.Sprintf("rag:doc:%s:%s:status", col, docID)
}

func docPrefix(col string) string {
	return fmt.Sprintf("rag:doc:%s:", col)
}

func taskKey(taskID string) string {
	return fmt.Sprintf("rag:task:%s", taskID)
}

func embCacheKey(hash string) string {
	return fmt.Sprintf("rag:cache:emb:%s", hash)
}

func chatKey(uid, sid string, seq int) string {
	return fmt.Sprintf("rag:chat:%s:%s:%08d", uid, sid, seq)
}

func chatPrefix(uid, sid string) string {
	return fmt.Sprintf("rag:chat:%s:%s:", uid, sid)
}

func qlogKey(col, reqID string) string {
	return fmt.Sprintf("rag:qlog:%s:%s", col, reqID)
}

// prefixRange 返回前缀扫描的 [start, end). 末尾追加 0xff 作为上界,
// 对 RAG 使用的可打印 ASCII key 是安全的 (真实 key 不会含 0xff).
func prefixRange(p string) (string, string) {
	return p, p + "\xff"
}

// DeletePrefix removes all keys with the given prefix via server-side DeleteRange.
// Prefer this over Scan+WriteBatch when wiping a collection / wiki namespace.
func (s *Store) DeletePrefix(prefix string) error {
	start, end := prefixRange(prefix)
	return s.DeleteRange(start, end)
}

// ---- 领域类型 ----

// DocumentMeta 文档元数据.
type DocumentMeta struct {
	DocID       string `json:"doc_id"`
	Col         string `json:"col"`
	Title       string `json:"title"`
	Source      string `json:"source"`        // 文件名或 "inline"
	ContentHash string `json:"content_hash"`  // sha256, 去重依据
	ChunkCount  int    `json:"chunk_count"`
	CreatedAt   int64  `json:"created_at"`
}

// ChunkRecord 单个文本块. Text 进 minikv, Embedding 只在内存 (SideIndex).
type ChunkRecord struct {
	ChunkID   string    `json:"chunk_id"` // col:docID:seq
	Col       string    `json:"col"`
	DocID     string    `json:"doc_id"`
	Seq       int       `json:"seq"`
	Heading   string    `json:"heading,omitempty"`
	Text      string    `json:"text"`
	Embedding []float32 `json:"-"` // 不落 minikv
}

// IngestTask 入库任务状态.
type IngestTask struct {
	TaskID    string `json:"task_id"`
	Col       string `json:"col"`
	DocID     string `json:"doc_id"`
	Status    string `json:"status"` // pending|running|success|failed
	Progress  int    `json:"progress"`
	Error     string `json:"error,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// ChatMessage 会话历史的一条消息.
type ChatMessage struct {
	Role       string   `json:"role"` // user|assistant
	Content    string   `json:"content"`
	Citations  []string `json:"citations,omitempty"`
	CreatedAt  int64    `json:"created_at"`
}

// QueryLog 检索/问答日志 (异步写).
type QueryLog struct {
	Col        string  `json:"col"`
	ReqID      string  `json:"req_id"`
	Query      string  `json:"query"`
	Hits       []string `json:"hits"`
	LatencyMS  int64   `json:"latency_ms"`
	CreatedAt  int64   `json:"created_at"`
}

// ---- 任务状态机 ----

const (
	TaskPending = "pending"
	TaskRunning = "running"
	TaskSuccess = "success"
	TaskFailed  = "failed"
)

// SaveTask 写任务状态.
func (s *Store) SaveTask(t *IngestTask) error {
	t.UpdatedAt = time.Now().Unix()
	return s.PutJSON(taskKey(t.TaskID), t)
}

// LoadTask 读任务状态.
func (s *Store) LoadTask(taskID string) (*IngestTask, error) {
	var t IngestTask
	ok, err := s.GetJSON(taskKey(taskID), &t)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("task not found")
	}
	return &t, nil
}

// ---- 文档/ chunk 持久化 ----

// SaveDocument 原子写入一个文档的 meta + status + 所有 chunk (WriteBatch TCP).
func (s *Store) SaveDocument(meta *DocumentMeta, chunks []ChunkRecord, status string) error {
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	ops := make([]data.BatchOp, 0, 2+len(chunks))
	ops = append(ops, data.BatchOp{Put: true, Key: docMetaKey(meta.Col, meta.DocID), Value: string(metaBytes)})
	for _, c := range chunks {
		cb, err := json.Marshal(c)
		if err != nil {
			return fmt.Errorf("marshal chunk %d: %w", c.Seq, err)
		}
		ops = append(ops, data.BatchOp{Put: true, Key: chunkKey(c.Col, c.DocID, c.Seq), Value: string(cb)})
	}
	ops = append(ops, data.BatchOp{Put: true, Key: docStatusKey(meta.Col, meta.DocID), Value: status})
	return s.kv.WriteBatch(ops)
}

// DeleteDocument 删除文档的 meta + status + 所有 chunk (WriteBatch).
func (s *Store) DeleteDocument(col, docID string) error {
	chunks, err := s.ListChunks(col, docID)
	if err != nil {
		return err
	}
	ops := make([]data.BatchOp, 0, 2+len(chunks))
	for _, c := range chunks {
		ops = append(ops, data.BatchOp{Put: false, Key: chunkKey(c.Col, c.DocID, c.Seq)})
	}
	ops = append(ops, data.BatchOp{Put: false, Key: docMetaKey(col, docID)})
	ops = append(ops, data.BatchOp{Put: false, Key: docStatusKey(col, docID)})
	return s.kv.WriteBatch(ops)
}

// DeleteCollection removes all rag:doc / rag:chunk / rag:qlog keys for a collection.
func (s *Store) DeleteCollection(col string) error {
	if err := s.DeletePrefix(docPrefix(col)); err != nil {
		return err
	}
	if err := s.DeletePrefix(fmt.Sprintf("rag:chunk:%s:", col)); err != nil {
		return err
	}
	return s.DeletePrefix(fmt.Sprintf("rag:qlog:%s:", col))
}

// ListChunks 列出某文档的全部 chunk, 按 seq 升序 (依赖 minikv Scan 的 key 有序性).
func (s *Store) ListChunks(col, docID string) ([]ChunkRecord, error) {
	start, end := prefixRange(chunkPrefix(col, docID))
	pairs, err := s.Scan(start, end)
	if err != nil {
		return nil, err
	}
	out := make([]ChunkRecord, 0, len(pairs))
	for _, p := range pairs {
		var c ChunkRecord
		if err := json.Unmarshal([]byte(p.Value), &c); err == nil {
			out = append(out, c)
		}
	}
	return out, nil
}

// ListDocuments 列出某 collection 的全部文档 meta.
func (s *Store) ListDocuments(col string) ([]DocumentMeta, error) {
	start, end := prefixRange(docPrefix(col))
	pairs, err := s.Scan(start, end)
	if err != nil {
		return nil, err
	}
	out := make([]DocumentMeta, 0)
	for _, p := range pairs {
		if !strings.HasSuffix(p.Key, ":meta") {
			continue
		}
		var m DocumentMeta
		if err := json.Unmarshal([]byte(p.Value), &m); err == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// AppendChat 追加一条会话历史. seq 由调用方保证递增.
func (s *Store) AppendChat(uid, sid, role, content string, citations []string, seq int) error {
	msg := ChatMessage{
		Role:      role,
		Content:   content,
		Citations: citations,
		CreatedAt: time.Now().Unix(),
	}
	return s.PutJSON(chatKey(uid, sid, seq), msg)
}

// ListChat 回放会话历史 (按 seq 升序).
func (s *Store) ListChat(uid, sid string) ([]ChatMessage, error) {
	start, end := prefixRange(chatPrefix(uid, sid))
	pairs, err := s.Scan(start, end)
	if err != nil {
		return nil, err
	}
	out := make([]ChatMessage, 0, len(pairs))
	for _, p := range pairs {
		var m ChatMessage
		if err := json.Unmarshal([]byte(p.Value), &m); err == nil {
			out = append(out, m)
		}
	}
	return out, nil
}
