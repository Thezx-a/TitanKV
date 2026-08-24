package rag

import (
	"testing"
)

func TestEvaluateEmpty(t *testing.T) {
	r := EvalResult{}
	if r.Queries != 0 {
		t.Fatal("expected zero queries")
	}
}

func TestRerankBoost(t *testing.T) {
	rr := NewReranker(true)
	hits := []RetrievalHit{
		{ChunkID: "c1", Score: 0.9, Text: "hello world"},
		{ChunkID: "c2", Score: 0.85, Text: "unrelated"},
	}
	out := rr.Rerank("hello", hits)
	if len(out) == 0 || out[0].ChunkID != "c1" {
		t.Fatalf("expected c1 first after rerank, got %+v", out)
	}
}
