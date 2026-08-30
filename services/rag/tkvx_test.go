package rag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTKVXRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.idx")
	vecs := map[string][]float32{
		"demo/d1/00000000": {1, 0, 0, 0},
		"demo/d1/00000001": {0, 1, 0, 0},
	}
	if err := WriteTKVXSnapshot(path, "demo", 4, "cosine", vecs); err != nil {
		t.Fatal(err)
	}
	got, meta, err := ReadTKVXSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Col != "demo" || meta.Dim != 4 || meta.Count != 2 {
		t.Fatalf("meta=%+v", meta)
	}
	if len(got) != 2 {
		t.Fatalf("vectors=%d", len(got))
	}
	if got["demo/d1/00000000"][0] < 0.99 {
		t.Fatalf("vec0=%v", got["demo/d1/00000000"])
	}
}

func TestTKVXCorruptCRC(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.idx")
	vecs := map[string][]float32{"a": {1, 0}}
	if err := WriteTKVXSnapshot(path, "c", 2, "cosine", vecs); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// flip a byte in the payload region (near end)
	b[len(b)-1] ^= 0xff
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = ReadTKVXSnapshot(path)
	if err != ErrCorruptSnapshot {
		t.Fatalf("want ErrCorruptSnapshot, got %v", err)
	}
}

func TestSideIndexSaveLoadTKVX(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "col.idx")
	idx := NewSideIndex(3)
	idx.Add("c/d/00000000", []float32{3, 0, 0})
	idx.Add("c/d/00000001", []float32{0, 4, 0})
	if err := idx.SaveSnapshotPrefix(path, "c/"); err != nil {
		t.Fatal(err)
	}
	// file should start with TKVX magic
	raw, _ := os.ReadFile(path)
	if string(raw[:4]) != "TKVX" {
		t.Fatalf("want TKVX magic, got %q", raw[:min(8, len(raw))])
	}
	idx2 := NewSideIndex(3)
	if err := idx2.MergeSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if idx2.Size() != 2 {
		t.Fatalf("size=%d", idx2.Size())
	}
	hits := idx2.TopK([]float32{1, 0, 0}, 1)
	if len(hits) == 0 || hits[0].ChunkID != "c/d/00000000" {
		t.Fatalf("hits=%+v", hits)
	}
}

func TestMergeSnapshotJSONLegacy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.idx")
	// write old JSON format directly
	if err := atomicWriteJSON(path, snapshotFile{
		Dim: 2,
		Vectors: map[string][]float32{
			"x/y/00000000": {0, 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	idx := NewSideIndex(2)
	if err := idx.MergeSnapshot(path); err != nil {
		t.Fatal(err)
	}
	if idx.Size() != 1 {
		t.Fatalf("legacy load size=%d", idx.Size())
	}
}
