package rag

import (
	"math"
	"strings"
	"sync"
)

// SideIndex 是向量旁路索引: chunk_id → 向量.
//
// 按 RagKv.md §10.1, 第一阶段内存 + 文件快照, 向量不进 minikv.
// 检索 TopK 后, 用 chunk_id 回查 minikv 拿 chunk text.
type SideIndex struct {
	mu      sync.RWMutex
	vectors map[string][]float32 // chunk_id → vec
	dim     int
	metric  string // "cosine"
}

// NewSideIndex 创建空索引. dim 必须与 Embedder.Dim() 一致.
func NewSideIndex(dim int) *SideIndex {
	return &SideIndex{
		vectors: make(map[string][]float32),
		dim:     dim,
		metric:  "cosine",
	}
}

// Dim 返回向量维度.
func (idx *SideIndex) Dim() int { return idx.dim }

// Size 返回当前向量数.
func (idx *SideIndex) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.vectors)
}

// Add 插入/更新一个 chunk 的向量 (预 L2 归一化, TopK 只需点积).
func (idx *SideIndex) Add(chunkID string, vec []float32) {
	if len(vec) != idx.dim {
		return
	}
	cp := l2Normalize(vec)
	idx.mu.Lock()
	idx.vectors[chunkID] = cp
	idx.mu.Unlock()
}

// Delete 删除一个 chunk 的向量.
func (idx *SideIndex) Delete(chunkID string) {
	idx.mu.Lock()
	delete(idx.vectors, chunkID)
	idx.mu.Unlock()
}

// ClearByPrefix 删除所有以 prefix 开头的 chunk_id (用于删整个文档/ collection).
func (idx *SideIndex) ClearByPrefix(prefix string) int {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	n := 0
	for k := range idx.vectors {
		if strings.HasPrefix(k, prefix) {
			delete(idx.vectors, k)
			n++
		}
	}
	return n
}

// Hit 单条检索命中.
type Hit struct {
	ChunkID string  `json:"chunk_id"`
	Score   float32 `json:"score"`
}

// TopK 返回与 query 最相似的 k 个 chunk_id (余弦相似度, 降序).
// 向量入库已预归一化, 此处对 query 归一化后走点积.
func (idx *SideIndex) TopK(query []float32, k int) []Hit {
	if k <= 0 {
		k = 5
	}
	q := l2Normalize(query)
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	type cand struct {
		id    string
		score float32
	}
	cands := make([]cand, 0, len(idx.vectors))
	for id, vec := range idx.vectors {
		cands = append(cands, cand{id, dotProduct(q, vec)})
	}
	if k > len(cands) {
		k = len(cands)
	}
	// 部分选择排序取 topK (避免全排序)
	for i := 0; i < k && i < len(cands); i++ {
		maxI := i
		for j := i + 1; j < len(cands); j++ {
			if cands[j].score > cands[maxI].score {
				maxI = j
			}
		}
		cands[i], cands[maxI] = cands[maxI], cands[i]
	}
	out := make([]Hit, 0, k)
	for i := 0; i < k; i++ {
		out = append(out, Hit{ChunkID: cands[i].id, Score: cands[i].score})
	}
	return out
}

// l2Normalize returns a copy scaled to unit L2 norm (zero vector → copy as-is).
func l2Normalize(v []float32) []float32 {
	cp := make([]float32, len(v))
	copy(cp, v)
	var n float64
	for _, x := range cp {
		n += float64(x) * float64(x)
	}
	if n <= 0 {
		return cp
	}
	inv := float32(1.0 / math.Sqrt(n))
	for i := range cp {
		cp[i] *= inv
	}
	return cp
}

func dotProduct(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

// cosine 余弦相似度. 输入未必归一化, 这里兜底归一化.
func cosine(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	return dotProduct(l2Normalize(a), l2Normalize(b))
}

// ---- 快照持久化 ----

type snapshotFile struct {
	Dim     int                 `json:"dim"`
	Vectors map[string][]float32 `json:"vectors"`
}

// SaveSnapshot 把全量索引序列化到 path (TKVX 二进制).
func (idx *SideIndex) SaveSnapshot(path string) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return WriteTKVXSnapshot(path, "", idx.dim, idx.metric, idx.vectors)
}

// SaveSnapshotPrefix 只保存 chunk_id 以 prefix 开头的向量 (按 collection 分片快照).
func (idx *SideIndex) SaveSnapshotPrefix(path, prefix string) error {
	idx.mu.RLock()
	sub := make(map[string][]float32)
	for k, v := range idx.vectors {
		if strings.HasPrefix(k, prefix) {
			sub[k] = v
		}
	}
	dim := idx.dim
	metric := idx.metric
	idx.mu.RUnlock()
	col := strings.TrimSuffix(prefix, "/")
	return WriteTKVXSnapshot(path, col, dim, metric, sub)
}

// LoadSnapshot 从 path 覆盖加载 (TKVX 优先, JSON 兼容).
func (idx *SideIndex) LoadSnapshot(path string) error {
	vecs, dim, err := loadSnapshotVectors(path)
	if err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if dim > 0 {
		idx.dim = dim
	}
	idx.vectors = make(map[string][]float32, len(vecs))
	for k, v := range vecs {
		idx.vectors[k] = l2Normalize(v)
	}
	return nil
}

// MergeSnapshot 从 path 合并加载到现有索引 (不覆盖, 启动时逐个 col 合并).
func (idx *SideIndex) MergeSnapshot(path string) error {
	vecs, dim, err := loadSnapshotVectors(path)
	if err != nil {
		return err
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if dim > 0 && idx.dim == 0 {
		idx.dim = dim
	}
	for k, v := range vecs {
		idx.vectors[k] = l2Normalize(v)
	}
	return nil
}
