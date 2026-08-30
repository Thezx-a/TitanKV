package rag

import (
	"testing"
)

func TestHNSWIndexUsesEfParams(t *testing.T) {
	h := NewHNSWIndexWithParams(4, HNSWParams{M: 8, EfConstruction: 40, EfSearch: 20})
	if h.efConstruction != 40 || h.efSearch != 20 {
		t.Fatalf("ef not set: construction=%d search=%d", h.efConstruction, h.efSearch)
	}
	// multi-add must not deadlock (uses Locked helpers under write lock)
	for i := 0; i < 20; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i/26))
		vec := []float32{float32(i), 1, 0, float32(20 - i)}
		h.Add(id, vec)
	}
	if h.Size() < 10 {
		t.Fatalf("size=%d", h.Size())
	}
	hits := h.TopK([]float32{5, 1, 0, 15}, 3)
	if len(hits) == 0 {
		t.Fatal("TopK empty")
	}
}

func TestNewVectorIndexWithParamsHNSW(t *testing.T) {
	idx := NewVectorIndexWithParams(8, "hnsw", HNSWParams{EfConstruction: 50, EfSearch: 25})
	h, ok := idx.(*HNSWIndex)
	if !ok {
		t.Fatalf("want *HNSWIndex, got %T", idx)
	}
	if h.efConstruction != 50 || h.efSearch != 25 {
		t.Fatalf("params not applied: %+v", *h)
	}
}
