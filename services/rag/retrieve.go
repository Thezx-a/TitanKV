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

// RetrieverConfig tunes retrieve-time quality features.
type RetrieverConfig struct {
	TopK             int
	EnableRewrite    bool
	EnableHyde       bool
	EnableMultiQuery bool
	MultiQueryN      int
	Chat             ChatProvider // optional; HyDE prefers LLM when set
}

// Retriever 检索器: embed(query) → SideIndex TopK → 并发回查 minikv 取 chunk text.
//
// 热路径 (RagKv.md §8.2): 多个并发 Get 不争写锁, BloomFilter 跳过无效 SSTable.
type Retriever struct {
	embedder         Embedder
	index            VectorIndex
	store            *Store
	reranker         *Reranker
	topK             int
	enableRewrite    bool
	enableHyde       bool
	enableMultiQuery bool
	multiQueryN      int
	chat             ChatProvider
}

// NewRetriever 构造检索器.
func NewRetriever(e Embedder, idx VectorIndex, s *Store, rr *Reranker, topK int) *Retriever {
	return NewRetrieverOpts(e, idx, s, rr, topK, false)
}

// NewRetrieverOpts allows enabling query rewrite.
func NewRetrieverOpts(e Embedder, idx VectorIndex, s *Store, rr *Reranker, topK int, enableRewrite bool) *Retriever {
	return NewRetrieverWithConfig(e, idx, s, rr, RetrieverConfig{
		TopK: topK, EnableRewrite: enableRewrite,
	})
}

// NewRetrieverWithConfig constructs a Retriever with HyDE / multi-query options.
func NewRetrieverWithConfig(e Embedder, idx VectorIndex, s *Store, rr *Reranker, cfg RetrieverConfig) *Retriever {
	if cfg.TopK <= 0 {
		cfg.TopK = 5
	}
	if cfg.MultiQueryN <= 0 {
		cfg.MultiQueryN = 3
	}
	return &Retriever{
		embedder: e, index: idx, store: s, reranker: rr,
		topK: cfg.TopK, enableRewrite: cfg.EnableRewrite,
		enableHyde: cfg.EnableHyde, enableMultiQuery: cfg.EnableMultiQuery,
		multiQueryN: cfg.MultiQueryN, chat: cfg.Chat,
	}
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

	queries := []string{query}
	if r.enableMultiQuery {
		queries = ExpandMultiQueries(query, r.multiQueryN)
	}
	if r.enableHyde {
		passage := HydePassage(query)
		// Skip local-mock: it does not produce useful HyDE passages.
		if r.chat != nil && r.chat.Provider() != "local-mock" {
			if gen, err := generateHydeWithChat(ctx, r.chat, query); err == nil && strings.TrimSpace(gen) != "" {
				passage = gen
			}
		}
		if passage != "" {
			queries = append(queries, passage)
		}
	}

	var lists [][]RetrievalHit
	for _, q := range queries {
		hits, err := r.retrieveOnce(ctx, col, q, topK)
		if err != nil {
			return nil, err
		}
		if len(hits) > 0 {
			lists = append(lists, hits)
		}
	}
	if len(lists) == 0 {
		return nil, nil
	}
	var out []RetrievalHit
	if len(lists) == 1 {
		out = lists[0]
	} else {
		out = FuseHitsByRRF(lists, topK)
	}
	if r.reranker != nil {
		out = r.reranker.Rerank(query, out)
	}
	return out, nil
}

func generateHydeWithChat(ctx context.Context, chat ChatProvider, query string) (string, error) {
	prompt := fmt.Sprintf(
		"写一段 80 字以内的中文说明性段落，直接回答或阐述：%s\n只输出段落正文。",
		query,
	)
	var b strings.Builder
	err := chat.StreamComplete(ctx, prompt, func(tok string) error {
		b.WriteString(tok)
		return nil
	})
	return strings.TrimSpace(b.String()), err
}

func (r *Retriever) retrieveOnce(ctx context.Context, col, query string, topK int) ([]RetrievalHit, error) {
	vec, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	prefix := col + "/"
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

	out := hits[:0]
	for _, h := range hits {
		if h.ChunkID != "" {
			out = append(out, h)
		}
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
