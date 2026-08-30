package rag

import (
	"testing"
)

func TestHNSWMarkDeletedSkipsTopK(t *testing.T) {
	h := NewHNSWIndexWithParams(2, HNSWParams{M: 8, EfConstruction: 32, EfSearch: 16})
	h.Add("keep", []float32{1, 0})
	h.Add("drop", []float32{0.9, 0.1})
	h.Delete("drop")
	hits := h.TopK([]float32{1, 0}, 5)
	for _, hit := range hits {
		if hit.ChunkID == "drop" {
			t.Fatalf("deleted node leaked into TopK: %+v", hits)
		}
	}
	if h.Size() != 1 {
		t.Fatalf("Size should exclude deleted, got %d", h.Size())
	}
}

func TestHNSWCompactDeleted(t *testing.T) {
	h := NewHNSWIndex(2)
	h.Add("a", []float32{1, 0})
	h.Add("b", []float32{0, 1})
	h.Delete("b")
	n := h.CompactDeleted()
	if n < 1 {
		t.Fatalf("compact removed=%d", n)
	}
	h.mu.RLock()
	_, still := h.nodes["b"]
	h.mu.RUnlock()
	if still {
		t.Fatal("b should be physically removed after compact")
	}
}
