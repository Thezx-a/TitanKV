package rag

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// HNSWIndex is a simplified Hierarchical NSW graph for approximate nearest neighbor search.
// Interface-compatible with SideIndex.TopK for drop-in use when N grows.
type HNSWIndex struct {
	mu      sync.RWMutex
	dim     int
	m       int
	ml      float64
	nodes   map[string]*hnswNode
	entry   string
	maxLayer int
}

type hnswNode struct {
	id     string
	vec    []float32
	layers [][]string
}

func NewHNSWIndex(dim int) *HNSWIndex {
	return &HNSWIndex{
		dim:   dim,
		m:     16,
		ml:    1.0 / math.Log(16),
		nodes: make(map[string]*hnswNode),
	}
}

func (h *HNSWIndex) Dim() int { return h.dim }

func (h *HNSWIndex) Size() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.nodes)
}

func (h *HNSWIndex) Add(id string, vec []float32) {
	if len(vec) != h.dim {
		return
	}
	cp := make([]float32, len(vec))
	copy(cp, vec)
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.randomLayer()
	node := &hnswNode{id: id, vec: cp, layers: make([][]string, l+1)}
	for i := range node.layers {
		node.layers[i] = make([]string, 0, h.m)
	}
	h.nodes[id] = node
	if h.entry == "" {
		h.entry = id
		h.maxLayer = l
		return
	}
	// Greedy search from top layer down, connect neighbors at each layer.
	curr := h.entry
	for lc := h.maxLayer; lc > l; lc-- {
		curr = h.greedyClosest(curr, cp, lc)
	}
	for lc := l; lc >= 0; lc-- {
		neighbors := h.searchLayer(cp, curr, h.m*2, lc)
		for _, nb := range neighbors {
			if nb == id {
				continue
			}
			node.layers[lc] = append(node.layers[lc], nb)
			nbNode := h.nodes[nb]
			if len(nbNode.layers[lc]) < h.m {
				nbNode.layers[lc] = append(nbNode.layers[lc], id)
			}
		}
		curr = neighbors[0]
	}
	if l > h.maxLayer {
		h.maxLayer = l
		h.entry = id
	}
}

func (h *HNSWIndex) Delete(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.nodes, id)
	if h.entry == id {
		for k := range h.nodes {
			h.entry = k
			break
		}
	}
}

func (h *HNSWIndex) ClearByPrefix(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for k := range h.nodes {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(h.nodes, k)
			n++
		}
	}
	return n
}

func (h *HNSWIndex) TopK(query []float32, k int) []Hit {
	if k <= 0 {
		k = 5
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.nodes) == 0 || h.entry == "" {
		return nil
	}
	curr := h.entry
	for lc := h.maxLayer; lc > 0; lc-- {
		curr = h.greedyClosestLocked(curr, query, lc)
	}
	cands := h.searchLayerLocked(query, curr, k*4, 0)
	if k > len(cands) {
		k = len(cands)
	}
	out := make([]Hit, 0, k)
	for i := 0; i < k; i++ {
		out = append(out, Hit{ChunkID: cands[i], Score: cosine(query, h.nodes[cands[i]].vec)})
	}
	return out
}

func (h *HNSWIndex) randomLayer() int {
	l := 0
	for rand.Float64() < 0.5 && l < 8 {
		l++
	}
	return l
}

func (h *HNSWIndex) greedyClosest(start string, q []float32, layer int) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.greedyClosestLocked(start, q, layer)
}

func (h *HNSWIndex) greedyClosestLocked(start string, q []float32, layer int) string {
	curr := start
	currScore := cosine(q, h.nodes[curr].vec)
	for {
		improved := false
		for _, nb := range h.nodes[curr].layers[layer] {
			if nbNode, ok := h.nodes[nb]; ok {
				s := cosine(q, nbNode.vec)
				if s > currScore {
					curr, currScore = nb, s
					improved = true
				}
			}
		}
		if !improved {
			return curr
		}
	}
}

func (h *HNSWIndex) searchLayer(q []float32, entry string, ef int, layer int) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.searchLayerLocked(q, entry, ef, layer)
}

func (h *HNSWIndex) searchLayerLocked(q []float32, entry string, ef int, layer int) []string {
	visited := map[string]bool{entry: true}
	results := []string{entry}
	candidates := []string{entry}
	for len(candidates) > 0 {
		bestI := 0
		bestS := cosine(q, h.nodes[candidates[0]].vec)
		for i, c := range candidates[1:] {
			s := cosine(q, h.nodes[c].vec)
			if s > bestS {
				bestI = i + 1
				bestS = s
			}
		}
		curr := candidates[bestI]
		candidates = append(candidates[:bestI], candidates[bestI+1:]...)
		for _, nb := range h.nodes[curr].layers[layer] {
			if visited[nb] {
				continue
			}
			visited[nb] = true
			results = append(results, nb)
			candidates = append(candidates, nb)
		}
		if len(results) > ef {
			// trim to ef best
			for i := 0; i < len(results)-1; i++ {
				for j := i + 1; j < len(results); j++ {
					if cosine(q, h.nodes[results[j]].vec) > cosine(q, h.nodes[results[i]].vec) {
						results[i], results[j] = results[j], results[i]
					}
				}
			}
			results = results[:ef]
		}
	}
	// sort descending by score
	for i := 0; i < len(results)-1; i++ {
		for j := i + 1; j < len(results); j++ {
			if cosine(q, h.nodes[results[j]].vec) > cosine(q, h.nodes[results[i]].vec) {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	return results
}

func (h *HNSWIndex) exportVectors() map[string][]float32 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string][]float32, len(h.nodes))
	for id, n := range h.nodes {
		cp := make([]float32, len(n.vec))
		copy(cp, n.vec)
		out[id] = cp
	}
	return out
}

func (h *HNSWIndex) SaveSnapshot(path string) error {
	return h.saveSnapshotFile(path, h.exportVectors())
}

func (h *HNSWIndex) SaveSnapshotPrefix(path, prefix string) error {
	all := h.exportVectors()
	sub := make(map[string][]float32)
	for k, v := range all {
		if strings.HasPrefix(k, prefix) {
			sub[k] = v
		}
	}
	return h.saveSnapshotFile(path, sub)
}

func (h *HNSWIndex) MergeSnapshot(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var sf snapshotFile
	if err := json.NewDecoder(f).Decode(&sf); err != nil {
		return err
	}
	for k, v := range sf.Vectors {
		h.Add(k, v)
	}
	return nil
}

func (h *HNSWIndex) saveSnapshotFile(path string, vecs map[string][]float32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(snapshotFile{Dim: h.dim, Vectors: vecs})
}

// VectorIndex abstracts brute SideIndex and HNSWIndex.
type VectorIndex interface {
	Dim() int
	Size() int
	Add(chunkID string, vec []float32)
	Delete(chunkID string)
	ClearByPrefix(prefix string) int
	TopK(query []float32, k int) []Hit
}

// SnapshotStore persists vector index to disk.
type SnapshotStore interface {
	SaveSnapshot(path string) error
	SaveSnapshotPrefix(path, prefix string) error
	MergeSnapshot(path string) error
}

// NewVectorIndex picks HNSW when RAG_INDEX_TYPE=hnsw, else brute-force SideIndex.
func NewVectorIndex(dim int, indexType string) VectorIndex {
	if indexType == "hnsw" {
		return NewHNSWIndex(dim)
	}
	return NewSideIndex(dim)
}
