package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// RetrievalHit 检索命中的完整记录 (含从 minikv 回查的 chunk 正文).
type RetrievalHit struct {
	ChunkID string  `json:"chunk_id"`
	Score   float32 `json:"score"`
	Col     string  `json:"col"`
	DocID   string  `json:"doc_id"`
	Seq     int     `json:"seq"`
	Heading string  `json:"heading,omitempty"`
	Text    string  `json:"text"`
}

// Retriever 检索器: embed(query) → SideIndex TopK → 并发回查 minikv 取 chunk text.
//
// 热路径 (RagKv.md §8.2): 多个并发 Get 不争写锁, BloomFilter 跳过无效 SSTable.
type Retriever struct {
	embedder      Embedder
	index         VectorIndex
	store         *Store
	reranker      *Reranker
	topK          int
	enableRewrite bool
}

// NewRetriever 构造检索器.
func NewRetriever(e Embedder, idx VectorIndex, s *Store, rr *Reranker, topK int) *Retriever {
	return NewRetrieverOpts(e, idx, s, rr, topK, false)
}

// NewRetrieverOpts allows enabling query rewrite.
func NewRetrieverOpts(e Embedder, idx VectorIndex, s *Store, rr *Reranker, topK int, enableRewrite bool) *Retriever {
	if topK <= 0 {
		topK = 5
	}
	return &Retriever{embedder: e, index: idx, store: s, reranker: rr, topK: topK, enableRewrite: enableRewrite}
}

// Retrieve 返回 col 内与 query 最相关的 topK 个 chunk (按 score 降序).
func (r *Retriever) Retrieve(ctx context.Context, col, query string, topK int) ([]RetrievalHit, error) {
	start := time.Now()
	defer func() { RagRetrieveDuration.Observe(time.Since(start).Seconds()) }()
	if topK <= 0 {
		topK = r.topK
	}
	if r.enableRewrite {
		query = RewriteQuery(query)
	}
	vec, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	// chunk_id 形如 "col/docID/seq", 前缀 "col/" 过滤本 collection
	prefix := col + "/"
	// 全局 TopK 多取些, 再按前缀过滤, 避免跨 col 干扰 (MVP 暴力扫描可接受)
	raw := r.index.TopK(vec, topK*4)
	cands := make([]Hit, 0, topK)
	for _, h := range raw {
		if strings.HasPrefix(h.ChunkID, prefix) {
			cands = append(cands, h)
			if len(cands) >= topK {
				break
			}
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}

	// 并发回查 minikv 取 chunk 正文
	hits := make([]RetrievalHit, len(cands))
	var wg sync.WaitGroup
	for i, h := range cands {
		wg.Add(1)
		go func(i int, h Hit) {
			defer wg.Done()
			col2, docID, seq, ok := parseChunkID(h.ChunkID)
			if !ok {
				return
			}
			raw, ok2, err := r.store.Get(chunkKey(col2, docID, seq))
			if err != nil || !ok2 {
				return
			}
			var c ChunkRecord
			if json.Unmarshal([]byte(raw), &c) != nil {
				return
			}
			hits[i] = RetrievalHit{
				ChunkID: h.ChunkID, Score: h.Score,
				Col: col2, DocID: docID, Seq: seq,
				Heading: c.Heading, Text: c.Text,
			}
		}(i, h)
	}
	wg.Wait()

	// 去掉未命中的空位, 重新按 score 降序
	out := hits[:0]
	for _, h := range hits {
		if h.ChunkID != "" {
			out = append(out, h)
		}
	}
	if r.reranker != nil {
		out = r.reranker.Rerank(query, out)
	}
	return out, nil
}

// parseChunkID 解析 "col/docID/seq" → (col, docID, seq).
// col / docID 假设不含 "/" (collection name 与 uuid 均满足).
func parseChunkID(id string) (col, docID string, seq int, ok bool) {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) != 3 {
		return "", "", 0, false
	}
	for _, r := range parts[2] {
		if r < '0' || r > '9' {
			return "", "", 0, false
		}
	}
	n := 0
	for _, r := range parts[2] {
		n = n*10 + int(r-'0')
	}
	return parts[0], parts[1], n, true
}
