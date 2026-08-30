package rag

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPRerankerUsesSidecarScores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"scores": []float64{0.1, 0.9},
		})
	}))
	defer srv.Close()

	rr := NewHTTPReranker(srv.URL, true)
	hits := []RetrievalHit{
		{ChunkID: "a", Text: "aaa", Score: 0.5},
		{ChunkID: "b", Text: "bbb", Score: 0.6},
	}
	out := rr.Rerank("q", hits)
	if out[0].ChunkID != "b" {
		t.Fatalf("want b first from sidecar scores, got %+v", out)
	}
}

func TestHTTPRerankerFallsBackLexical(t *testing.T) {
	rr := NewHTTPReranker("http://127.0.0.1:1", true) // unreachable
	hits := []RetrievalHit{
		{ChunkID: "a", Text: "zzz unrelated", Score: 0.5},
		{ChunkID: "b", Text: "hello world", Score: 0.4},
	}
	out := rr.Rerank("hello", hits)
	// lexical should boost b over a when vector scores are close
	if out[0].ChunkID != "b" {
		t.Fatalf("fallback lexical want b first, got %+v", out)
	}
}
