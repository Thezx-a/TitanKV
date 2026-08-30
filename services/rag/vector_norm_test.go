package rag

import (
	"math"
	"testing"
)

func TestSideIndexAddPreNormalizes(t *testing.T) {
	idx := NewSideIndex(3)
	idx.Add("a", []float32{3, 0, 0})
	idx.mu.RLock()
	v := idx.vectors["a"]
	idx.mu.RUnlock()
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if math.Abs(n-1.0) > 1e-5 {
		t.Fatalf("stored vector should be L2-normalized, norm²=%v vec=%v", n, v)
	}
}

func TestSideIndexTopKUsesDotOnNormalized(t *testing.T) {
	idx := NewSideIndex(2)
	idx.Add("near", []float32{1, 0})
	idx.Add("far", []float32{0, 1})
	hits := idx.TopK([]float32{2, 0}, 2) // unnormalized query; Add/TopK should normalize
	if len(hits) < 1 || hits[0].ChunkID != "near" {
		t.Fatalf("want near first, got %+v", hits)
	}
	if hits[0].Score < 0.99 {
		t.Fatalf("cosine of parallel vectors should be ~1, got %v", hits[0].Score)
	}
}

func TestHNSWAddPreNormalizes(t *testing.T) {
	h := NewHNSWIndex(3)
	h.Add("a", []float32{0, 4, 0})
	h.mu.RLock()
	nodes := h.nodes["a"]
	h.mu.RUnlock()
	if nodes == nil {
		t.Fatal("node missing")
	}
	var n float64
	for _, x := range nodes.vec {
		n += float64(x) * float64(x)
	}
	if math.Abs(n-1.0) > 1e-5 {
		t.Fatalf("HNSW stored vec should be normalized, norm²=%v", n)
	}
}
