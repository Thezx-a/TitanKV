package rag

import (
	"math"
	"math/rand"
	"strings"
	"sync"
)

// HNSWIndex is a simplified Hierarchical NSW graph for approximate nearest neighbor search.
// Interface-compatible with SideIndex.TopK for drop-in use when N grows.
type HNSWIndex struct {
	mu             sync.RWMutex
	dim            int
	m              int
	ml             float64
	efConstruction int
	efSearch       int
	nodes          map[string]*hnswNode
	entry          string
	maxLayer       int
}

// HNSWParams tunes graph build/search (T2.10).
type HNSWParams struct {
	M              int
	EfConstruction int
	EfSearch       int
}

type hnswNode struct {
	id      string
	vec     []float32
	layers  [][]string
	deleted bool
}

func NewHNSWIndex(dim int) *HNSWIndex {
	return NewHNSWIndexWithParams(dim, HNSWParams{})
}

// NewHNSWIndexWithParams constructs HNSW with tunable efConstruction/efSearch.
func NewHNSWIndexWithParams(dim int, p HNSWParams) *HNSWIndex {
	m := p.M
	if m <= 0 {
		m = 16
	}
	efC := p.EfConstruction
	if efC <= 0 {
		efC = 200
	}
	efS := p.EfSearch
	if efS <= 0 {
		efS = 100
	}
	return &HNSWIndex{
		dim:            dim,
		m:              m,
		ml:             1.0 / math.Log(float64(m)),
		efConstruction: efC,
		efSearch:       efS,
		nodes:          make(map[string]*hnswNode),
	}
}

func (h *HNSWIndex) Dim() int { return h.dim }

func (h *HNSWIndex) Size() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, node := range h.nodes {
		if !node.deleted {
			n++
		}
	}
	return n
}

func (h *HNSWIndex) Add(id string, vec []float32) {
	if len(vec) != h.dim {
		return
	}
	cp := l2Normalize(vec)
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.randomLayer()
	node := &hnswNode{id: id, vec: cp, layers: make([][]string, l+1)}
	for i := range node.layers {
		node.layers[i] = make([]string, 0, h.m)
	}
	if old, ok := h.nodes[id]; ok && old.deleted {
		// revive in place
		old.vec = cp
		old.deleted = false
		old.layers = node.layers
		node = old
		h.nodes[id] = old
	} else {
		h.nodes[id] = node
	}
	if h.entry == "" {
		h.entry = id
		h.maxLayer = l
		return
	}
	// Greedy search from top layer down, connect neighbors at each layer.
	// MUST use Locked helpers — we already hold h.mu.Lock (avoid RWMutex re-entry deadlock).
	curr := h.entry
	for lc := h.maxLayer; lc > l; lc-- {
		curr = h.greedyClosestLocked(curr, cp, lc)
	}
	ef := h.efConstruction
	if ef < h.m*2 {
		ef = h.m * 2
	}
	// Only search/connect on layers that already exist in the graph.
	startLc := l
	if startLc > h.maxLayer {
		startLc = h.maxLayer
	}
	for lc := startLc; lc >= 0; lc-- {
		neighbors := h.searchLayerLocked(cp, curr, ef, lc)
		if len(neighbors) == 0 {
			continue
		}
		for _, nb := range neighbors {
			if nb == id {
				continue
			}
			nbNode, ok := h.nodes[nb]
			if !ok || lc >= len(nbNode.layers) {
				continue
			}
			node.layers[lc] = append(node.layers[lc], nb)
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
	n, ok := h.nodes[id]
	if !ok {
		return
	}
	n.deleted = true
	if h.entry == id {
		h.entry = ""
		for k, node := range h.nodes {
			if !node.deleted {
				h.entry = k
				break
			}
		}
	}
}

// CompactDeleted physically removes mark-deleted nodes and purges neighbor refs.
func (h *HNSWIndex) CompactDeleted() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for id, node := range h.nodes {
		if node.deleted {
			delete(h.nodes, id)
			n++
		}
	}
	for _, node := range h.nodes {
		for li := range node.layers {
			kept := node.layers[li][:0]
			for _, nb := range node.layers[li] {
				if other, ok := h.nodes[nb]; ok && !other.deleted {
					kept = append(kept, nb)
				}
			}
			node.layers[li] = kept
		}
	}
	if h.entry != "" {
		if e, ok := h.nodes[h.entry]; !ok || e.deleted {
			h.entry = ""
			for k, node := range h.nodes {
				if !node.deleted {
					h.entry = k
					break
				}
			}
		}
	}
	return n
}

func (h *HNSWIndex) ClearByPrefix(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for k, node := range h.nodes {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix && !node.deleted {
			node.deleted = true
			n++
		}
	}
	if h.entry != "" {
		if e, ok := h.nodes[h.entry]; !ok || e.deleted {
			h.entry = ""
			for k, node := range h.nodes {
				if !node.deleted {
					h.entry = k
					break
				}
			}
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
	q := l2Normalize(query)
	curr := h.entry
	for lc := h.maxLayer; lc > 0; lc-- {
		curr = h.greedyClosestLocked(curr, q, lc)
	}
	ef := h.efSearch
	if ef < k*4 {
		ef = k * 4
	}
	cands := h.searchLayerLocked(q, curr, ef, 0)
	if k > len(cands) {
		k = len(cands)
	}
	out := make([]Hit, 0, k)
	for _, id := range cands {
		n := h.nodes[id]
		if n == nil || n.deleted {
			continue
		}
		out = append(out, Hit{ChunkID: id, Score: dotProduct(q, n.vec)})
		if len(out) >= k {
			break
		}
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
	nodes := h.nodes[curr]
	if nodes == nil {
		return start
	}
	currScore := cosine(q, nodes.vec)
	for {
		improved := false
		for _, nb := range nodeLayers(h.nodes[curr], layer) {
			if nbNode, ok := h.nodes[nb]; ok && !nbNode.deleted {
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

func nodeLayers(n *hnswNode, layer int) []string {
	if n == nil || layer < 0 || layer >= len(n.layers) {
		return nil
	}
	return n.layers[layer]
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
		for _, nb := range nodeLayers(h.nodes[curr], layer) {
			if visited[nb] {
				continue
			}
			nbNode, ok := h.nodes[nb]
			if !ok || nbNode.deleted {
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
		if n.deleted {
			continue
		}
		cp := make([]float32, len(n.vec))
		copy(cp, n.vec)
		out[id] = cp
	}
	return out
}

// exportGraph copies adjacency for live (non-deleted) nodes.
func (h *HNSWIndex) exportGraph() map[string][][]string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string][][]string, len(h.nodes))
	for id, n := range h.nodes {
		if n.deleted {
			continue
		}
		layers := make([][]string, len(n.layers))
		for i, L := range n.layers {
			cp := make([]string, len(L))
			copy(cp, L)
			layers[i] = cp
		}
		out[id] = layers
	}
	return out
}

// importGraphLocked restores nodes from vectors+graph (caller holds write lock).
func (h *HNSWIndex) importGraphLocked(vecs map[string][]float32, g *TKVXGraph) {
	h.nodes = make(map[string]*hnswNode, len(vecs))
	h.entry = ""
	h.maxLayer = 0
	if g != nil {
		h.entry = g.Entry
		h.maxLayer = g.MaxLayer
	}
	for id, vec := range vecs {
		cp := l2Normalize(vec)
		var layers [][]string
		if g != nil {
			if L, ok := g.Nodes[id]; ok {
				layers = make([][]string, len(L))
				for i := range L {
					cpL := make([]string, len(L[i]))
					copy(cpL, L[i])
					layers[i] = cpL
				}
			}
		}
		if layers == nil {
			layers = [][]string{nil} // at least layer 0
		}
		h.nodes[id] = &hnswNode{id: id, vec: cp, layers: layers}
	}
	// fix entry if missing
	if h.entry != "" {
		if _, ok := h.nodes[h.entry]; !ok {
			h.entry = ""
		}
	}
	if h.entry == "" {
		for id := range h.nodes {
			h.entry = id
			break
		}
	}
	// recompute maxLayer from nodes
	maxL := 0
	for _, n := range h.nodes {
		if len(n.layers)-1 > maxL {
			maxL = len(n.layers) - 1
		}
	}
	h.maxLayer = maxL
}

func (h *HNSWIndex) SaveSnapshot(path string) error {
	return h.saveSnapshotFile(path, "", h.exportVectors(), h.exportGraph())
}

func (h *HNSWIndex) SaveSnapshotPrefix(path, prefix string) error {
	h.mu.RLock()
	vecs := make(map[string][]float32)
	graphNodes := make(map[string][][]string)
	entry := h.entry
	maxLayer := h.maxLayer
	for id, n := range h.nodes {
		if n.deleted || !strings.HasPrefix(id, prefix) {
			continue
		}
		cp := make([]float32, len(n.vec))
		copy(cp, n.vec)
		vecs[id] = cp
		layers := make([][]string, len(n.layers))
		for i, L := range n.layers {
			cpL := make([]string, 0, len(L))
			for _, nb := range L {
				if strings.HasPrefix(nb, prefix) {
					cpL = append(cpL, nb)
				}
			}
			layers[i] = cpL
		}
		graphNodes[id] = layers
	}
	h.mu.RUnlock()
	col := strings.TrimSuffix(prefix, "/")
	if entry != "" && !strings.HasPrefix(entry, prefix) {
		entry = ""
		for id := range vecs {
			entry = id
			break
		}
	}
	g := &TKVXGraph{Entry: entry, MaxLayer: maxLayer, Nodes: graphNodes}
	return WriteTKVXSnapshotEx(path, col, h.dim, "cosine", vecs, g)
}

func (h *HNSWIndex) MergeSnapshot(path string) error {
	vecs, meta, graph, err := loadSnapshotFull(path)
	if err != nil {
		return err
	}
	if graph != nil && meta.HasGraph {
		h.mu.Lock()
		h.importGraphLocked(vecs, graph)
		h.mu.Unlock()
		return nil
	}
	// v1 / JSON: rebuild via Add
	for k, v := range vecs {
		h.Add(k, v)
	}
	return nil
}

func (h *HNSWIndex) saveSnapshotFile(path, col string, vecs map[string][]float32, graphNodes map[string][][]string) error {
	if col == "" {
		for k := range vecs {
			if i := strings.IndexByte(k, '/'); i > 0 {
				col = k[:i]
				break
			}
		}
	}
	h.mu.RLock()
	entry, maxLayer := h.entry, h.maxLayer
	h.mu.RUnlock()
	g := &TKVXGraph{Entry: entry, MaxLayer: maxLayer, Nodes: graphNodes}
	return WriteTKVXSnapshotEx(path, col, h.dim, "cosine", vecs, g)
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
	return NewVectorIndexWithParams(dim, indexType, HNSWParams{})
}

// NewVectorIndexWithParams allows tuning HNSW ef params.
func NewVectorIndexWithParams(dim int, indexType string, p HNSWParams) VectorIndex {
	if indexType == "hnsw" {
		return NewHNSWIndexWithParams(dim, p)
	}
	return NewSideIndex(dim)
}
