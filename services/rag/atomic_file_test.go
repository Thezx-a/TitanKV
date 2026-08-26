package rag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFileReplacesAndSurvives(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "col.idx")

	if err := atomicWriteFile(path, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"v":1}` {
		t.Fatalf("got %q", b)
	}
	// No leftover .tmp
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp should be gone, err=%v", err)
	}

	if err := atomicWriteFile(path, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"v":2}` {
		t.Fatalf("got %q", b)
	}
}

func TestAtomicWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.idx")
	type sf struct {
		Dim int `json:"dim"`
	}
	if err := atomicWriteJSON(path, sf{Dim: 8}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if len(b) == 0 {
		t.Fatal("empty")
	}
}
