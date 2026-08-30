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
// 流程: 解析→清洗→去重→切块→批量 embedding→WriteBatch(minikv)→更新 SideIndex→快照.
// HTTP 层可同步调用 Ingest, 或经 IngestPool 异步调度.
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
	return g.runIngest(ctx, task, title, source, text)
}

// IngestWithTask continues a pre-created pending task (async path).
func (g *Ingester) IngestWithTask(ctx context.Context, task *IngestTask, title, source, text string) (*IngestTask, error) {
	if task == nil {
		return nil, fmt.Errorf("nil task")
	}
	return g.runIngest(ctx, task, title, source, text)
}

func (g *Ingester) runIngest(ctx context.Context, task *IngestTask, title, source, text string) (*IngestTask, error) {
	col, docID := task.Col, task.DocID
	now := time.Now().Unix()

	// 1) 去重 (content_hash 相同且 chunker_version 一致才跳过)
	h := sha256.Sum256([]byte(text))
	contentHash := hex.EncodeToString(h[:])
	if existing := g.findByHash(col, contentHash); existing != nil && existing.ChunkerVersion == ActiveChunkerVersion(CurrentTokenizerMode()) {
		task.Status = TaskSuccess
		task.Progress = 100
		task.DocID = existing.DocID
		_ = g.store.SaveTask(task)
		RagIngestTotal.WithLabelValues("dedup").Inc()
		return task, nil
	}

	task.Status = TaskRunning
	_ = g.store.SaveTask(task)

	// 2) 切块 (按 source 路由; 保留构造时 Size/Overlap)
	chunker := ChunkerFor(source, detectDocTypeFromText(source, text))
	chunker.Size = g.chunker.Size
	chunker.Overlap = g.chunker.Overlap
	chunks := chunker.Split(text)
	if len(chunks) == 0 {
		RagIngestTotal.WithLabelValues("failed").Inc()
		return g.failTask(task, "empty document after chunking")
	}

	// 3) 批量 embedding
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	vecs, err := EmbedTexts(ctx, g.embedder, texts, g.cfg.EmbeddingBatch)
	if err != nil {
		RagIngestTotal.WithLabelValues("failed").Inc()
		return g.failTask(task, fmt.Sprintf("embed: %v", err))
	}
	if len(vecs) != len(chunks) {
		RagIngestTotal.WithLabelValues("failed").Inc()
		return g.failTask(task, "embed batch size mismatch")
	}

	records := make([]ChunkRecord, 0, len(chunks))
	vectors := make(map[string][]float32, len(chunks))
	for i, c := range chunks {
		cid := chunkID(col, docID, c.Seq)
		cp := make([]float32, len(vecs[i]))
		copy(cp, vecs[i])
		vectors[cid] = cp
		records = append(records, ChunkRecord{
			ChunkID: cid, Col: col, DocID: docID, Seq: c.Seq,
			Heading: c.Heading, Text: c.Text,
		})
	}

	// 4) 持久化 WriteBatch
	meta := &DocumentMeta{
		DocID: docID, Col: col, Title: title, Source: source,
		ContentHash: contentHash, ChunkerVersion: ActiveChunkerVersion(CurrentTokenizerMode()),
		ChunkCount: len(records), CreatedAt: now,
	}
	if err := g.store.SaveDocument(meta, records, TaskRunning); err != nil {
		RagIngestTotal.WithLabelValues("failed").Inc()
		return g.failTask(task, err.Error())
	}
	for cid, vec := range vectors {
		g.index.Add(cid, vec)
	}
	if err := g.store.Put(docStatusKey(col, docID), TaskSuccess); err != nil {
		RagIngestTotal.WithLabelValues("failed").Inc()
		return g.failTask(task, err.Error())
	}

	_ = g.saveSnapshot(col)
	RagIndexSize.Set(float64(g.index.Size()))

	task.Status = TaskSuccess
	task.Progress = 100
	_ = g.store.SaveTask(task)
	RagIngestTotal.WithLabelValues("success").Inc()
	return task, nil
}

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

func (g *Ingester) saveSnapshot(col string) error {
	path := fmt.Sprintf("%s/%s.idx", g.cfg.IndexDir, col)
	if snap, ok := g.index.(SnapshotStore); ok {
		return snap.SaveSnapshotPrefix(path, col+"/")
	}
	return nil
}

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
