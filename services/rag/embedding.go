package rag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Embedder 把文本映射到定长稠密向量. 向量只进 SideIndex, 不进 minikv.
type Embedder interface {
	// Embed 返回归一化后的向量.
	Embed(ctx context.Context, text string) ([]float32, error)
	// Dim 返回向量维度.
	Dim() int
}

// BatchEmbedder optionally embeds many texts in one call (OpenAI batch).
type BatchEmbedder interface {
	Embedder
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

// EmbedTexts batches embedding. Uses BatchEmbedder when available; otherwise
// loops in groups of batchSize (default 32).
func EmbedTexts(ctx context.Context, e Embedder, texts []string, batchSize int) ([][]float32, error) {
	if batchSize <= 0 {
		batchSize = 32
	}
	if len(texts) == 0 {
		return nil, nil
	}
	if be, ok := e.(BatchEmbedder); ok {
		out := make([][]float32, 0, len(texts))
		for i := 0; i < len(texts); i += batchSize {
			end := i + batchSize
			if end > len(texts) {
				end = len(texts)
			}
			part, err := be.EmbedTexts(ctx, texts[i:end])
			if err != nil {
				return nil, err
			}
			out = append(out, part...)
		}
		return out, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := e.Embed(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("embed[%d]: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// ---- hashEmbedder: 无外部依赖的确定性伪 embedding ----
//
// 词袋 + FNV-1a: 按非空 token 分词, 每个 token hash 到一维 (mod dim),
// 累加该维, 最后 L2 归一化. 共享词多的文本余弦相似度高.
// 仅用于项目可独立运行; 接入真实模型时换 openAIEmbedder.
type hashEmbedder struct {
	dim int
}

// NewHashEmbedder 构造 hash 伪 embedder.
func NewHashEmbedder(dim int) Embedder {
	if dim <= 0 {
		dim = 384
	}
	return &hashEmbedder{dim: dim}
}

func (e *hashEmbedder) Dim() int { return e.dim }

func (e *hashEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, e.dim)
	for _, tok := range tokenize(text) {
		h := fnv.New32a()
		h.Write([]byte(tok))
		idx := int(h.Sum32()) % e.dim
		vec[idx] += 1.0
	}
	var sum float32
	for _, v := range vec {
		sum += v * v
	}
	if sum > 0 {
		inv := 1.0 / float32(math.Sqrt(float64(sum)))
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec, nil
}

// tokenize 简单分词: 小写化后按非字母数字切分, 过滤空 token.
func tokenize(text string) []string {
	text = strings.ToLower(text)
	out := make([]string, 0, 16)
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 0x80 {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// ---- openAIEmbedder: OpenAI 兼容 /v1/embeddings ----
type openAIEmbedder struct {
	apiBase string
	apiKey  string
	model   string
	dim     int
	http    *http.Client
}

// NewOpenAIEmbedder 构造走 OpenAI 兼容 API 的 embedder.
func NewOpenAIEmbedder(apiBase, apiKey, model string, dim int) Embedder {
	if dim <= 0 {
		dim = 1536
	}
	return &openAIEmbedder{
		apiBase: strings.TrimRight(apiBase, "/"),
		apiKey:  apiKey,
		model:   model,
		dim:     dim,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (e *openAIEmbedder) Dim() int { return e.dim }

type embedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (e *openAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	out, err := e.EmbedTexts(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("openai embedder: empty response")
	}
	return out[0], nil
}

func (e *openAIEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	if e.apiKey == "" {
		return nil, fmt.Errorf("openai embedder: RAG_EMBEDDING_API_KEY not set")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	var input any = texts
	if len(texts) == 1 {
		input = texts[0]
	}
	body, _ := json.Marshal(map[string]any{"model": e.model, "input": input})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.apiBase+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai embedder: http %d: %s", resp.StatusCode, string(b))
	}
	var out embedResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("openai embedder: decode: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("openai embedder: got %d vectors for %d texts", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

// ---- 缓存装饰器: query 向量缓存到 minikv, 命中跳过远端 ----

type cachedEmbedder struct {
	inner Embedder
	store *Store
	mu    sync.Mutex
}

// NewCachedEmbedder 用 minikv 缓存 embedding 结果 (rag:cache:emb:{sha256}).
func NewCachedEmbedder(inner Embedder, store *Store) Embedder {
	return &cachedEmbedder{inner: inner, store: store}
}

func (c *cachedEmbedder) Dim() int { return c.inner.Dim() }

func (c *cachedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	h := sha256.Sum256([]byte(text))
	key := embCacheKey(hex.EncodeToString(h[:]))
	if raw, ok, err := c.store.Get(key); err == nil && ok {
		var vec []float32
		if json.Unmarshal([]byte(raw), &vec) == nil {
			return vec, nil
		}
	}
	vec, err := c.inner.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	// 缓存写失败不影响主流程
	if buf, e := json.Marshal(vec); e == nil {
		_ = c.store.Put(key, string(buf))
	}
	return vec, nil
}
