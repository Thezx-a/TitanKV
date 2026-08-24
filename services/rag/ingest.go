package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Ingester 文档入库编排 (RagKv.md §8.1).
//
// MVP 为同步实现 (不开异步 goroutine), 流程仍按:
// 解析→清洗→去重→切块→批量 embedding→WriteBatch(minikv)→更新 SideIndex→快照.
type Ingester struct {
	store    *Store
	chunker  *Chunker
	embedder Embedder
	index    VectorIndex
	cfg      Config
}

// NewIngester 构造入库器.
func NewIngester(s *Store, ch *Chunker, e Embedder, idx VectorIndex, cfg Config) *Ingester {
	return &Ingester{store: s, chunker: ch, embedder: e, index: idx, cfg: cfg}
}

// chunkID 编码: "col/docID/seq", 作为 SideIndex 的 key (与 parseChunkID 对齐).
func chunkID(col, docID string, seq int) string {
	return fmt.Sprintf("%s/%s/%08d", col, docID, seq)
}

// Ingest 同步入库. 返回 IngestTask (status=success 或 failed).
// docID 为空时自动生成 uuid.
func (g *Ingester) Ingest(ctx context.Context, col, docID, title, source, text string) (*IngestTask, error) {
	if docID == "" {
		docID = uuid.NewString()
	}
	now := time.Now().Unix()
	task := &IngestTask{
		TaskID: uuid.NewString(), Col: col, DocID: docID,
		Status: TaskPending, Progress: 0, CreatedAt: now, UpdatedAt: now,
	}
	_ = g.store.SaveTask(task)

	// 1) 去重: 同 collection 内 content_hash 命中则直接返回旧 doc
	h := sha256.Sum256([]byte(text))
	contentHash := hex.EncodeToString(h[:])
	if existing := g.findByHash(col, contentHash); existing != nil {
		task.Status = TaskSuccess
		task.Progress = 100
		task.DocID = existing.DocID
		_ = g.store.SaveTask(task)
		return task, nil
	}

	task.Status = TaskRunning
	_ = g.store.SaveTask(task)

	// 2) 切块
	chunks := g.chunker.Split(text)
	if len(chunks) == 0 {
		return g.failTask(task, "empty document after chunking")
	}

	// 3) 批量 embedding + 组装 ChunkRecord (向量暂存, 先写 minikv 再进 SideIndex)
	records := make([]ChunkRecord, 0, len(chunks))
	vectors := make(map[string][]float32, len(chunks))
	for i, c := range chunks {
		vec, err := g.embedder.Embed(ctx, c.Text)
		if err != nil {
			return g.failTask(task, fmt.Sprintf("embed chunk %d: %v", i, err))
		}
		cid := chunkID(col, docID, c.Seq)
		cp := make([]float32, len(vec))
		copy(cp, vec)
		vectors[cid] = cp
		records = append(records, ChunkRecord{
			ChunkID: cid, Col: col, DocID: docID, Seq: c.Seq,
			Heading: c.Heading, Text: c.Text,
		})
	}

	// 4) 持久化: chunk 正文 + meta + status 进 minikv (WriteBatch 原子)
	meta := &DocumentMeta{
		DocID: docID, Col: col, Title: title, Source: source,
		ContentHash: contentHash, ChunkCount: len(records), CreatedAt: now,
	}
	if err := g.store.SaveDocument(meta, records, TaskRunning); err != nil {
		return g.failTask(task, err.Error())
	}
	// minikv 成功后更新 SideIndex (双写一致性: 先 KV 后向量)
	for cid, vec := range vectors {
		g.index.Add(cid, vec)
	}
	if err := g.store.Put(docStatusKey(col, docID), TaskSuccess); err != nil {
		return g.failTask(task, err.Error())
	}

	// 5) 写 SideIndex 快照 (失败不阻塞, 重启可从 minikv 重建)
	_ = g.saveSnapshot(col)

	task.Status = TaskSuccess
	task.Progress = 100
	_ = g.store.SaveTask(task)
	return task, nil
}

// findByHash 在 collection 内找 content_hash 相同的文档 (去重).
func (g *Ingester) findByHash(col, hash string) *DocumentMeta {
	docs, err := g.store.ListDocuments(col)
	if err != nil {
		return nil
	}
	for i := range docs {
		if docs[i].ContentHash == hash {
			return &docs[i]
		}
	}
	return nil
}

// saveSnapshot 写当前 collection 的 SideIndex 快照.
// 只导出本 col 的向量 (chunk_id 前缀 "col/").
func (g *Ingester) saveSnapshot(col string) error {
	path := fmt.Sprintf("%s/%s.idx", g.cfg.IndexDir, col)
	if snap, ok := g.index.(SnapshotStore); ok {
		return snap.SaveSnapshotPrefix(path, col+"/")
	}
	return nil
}

// failTask 置任务失败并返回 error.
func (g *Ingester) failTask(t *IngestTask, msg string) (*IngestTask, error) {
	t.Status = TaskFailed
	t.Error = msg
	_ = g.store.SaveTask(t)
	return t, fmt.Errorf("ingest failed: %s", msg)
}

// ResumeIngest retries a failed/pending task by task_id (idempotent re-ingest).
func (g *Ingester) ResumeIngest(ctx context.Context, taskID string) (*IngestTask, error) {
	task, err := g.store.LoadTask(taskID)
	if err != nil {
		return nil, err
	}
	if task.Status == TaskSuccess {
		return task, nil
	}
	// Re-run from stored doc metadata if chunks exist on disk
	chunks, _ := g.store.ListChunks(task.Col, task.DocID)
	if len(chunks) > 0 {
		task.Status = TaskSuccess
		task.Progress = 100
		_ = g.store.SaveTask(task)
		return task, nil
	}
	task.Status = TaskPending
	task.Error = ""
	_ = g.store.SaveTask(task)
	return task, fmt.Errorf("task %s has no persisted chunks; re-submit document", taskID)
}
func ensureColName(col string) error {
	if col == "" || strings.ContainsAny(col, "/:") {
		return fmt.Errorf("invalid collection name: %q", col)
	}
	return nil
}
