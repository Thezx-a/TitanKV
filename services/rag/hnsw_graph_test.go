package rag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHNSWGraphSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.idx")

	h := NewHNSWIndexWithParams(4, HNSWParams{M: 8, EfConstruction: 40, EfSearch: 20})
	ids := []string{"c/d/00000000", "c/d/00000001", "c/d/00000002", "c/d/00000003", "c/d/00000004"}
	for i, id := range ids {
		vec := []float32{float32(i), 1, 0, float32(4 - i)}
		h.Add(id, vec)
	}
	// capture adjacency before save
	before := h.exportGraph()
	if len(before) < 3 {
		t.Fatalf("graph too small: %d", len(before))
	}
	if err := h.SaveSnapshot(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw[:4]) != "TKVX" {
		t.Fatal("want TKVX")
	}

	h2 := NewHNSWIndexWithParams(4, HNSWParams{M: 8, EfConstruction: 40, EfSearch: 20})
	if err := h2.MergeSnapshot(path); err != nil {
		t.Fatal(err)
	}
	after := h2.exportGraph()
	if len(after) != len(before) {
		t.Fatalf("nodes before=%d after=%d", len(before), len(after))
	}
	// adjacency must match (not rebuilt via random Add)
	for id, layers := range before {
		got, ok := after[id]
		if !ok {
			t.Fatalf("missing node %s", id)
		}
		if len(got) != len(layers) {
			t.Fatalf("%s layers %d vs %d", id, len(got), len(layers))
		}
		for li := range layers {
			if !sameStringSet(layers[li], got[li]) {
				t.Fatalf("%s layer %d neighbors mismatch: %v vs %v", id, li, layers[li], got[li])
			}
		}
	}
	hits := h2.TopK([]float32{0, 1, 0, 4}, 2)
	if len(hits) == 0 {
		t.Fatal("TopK empty after graph load")
	}
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		if m[s] == 0 {
			return false
		}
		m[s]--
	}
	return true
}
