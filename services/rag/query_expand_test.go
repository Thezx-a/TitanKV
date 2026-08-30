package rag

import (
	"context"
	"strings"
	"testing"
)

func TestExpandMultiQueriesIncludesOriginal(t *testing.T) {
	q := "TitanKV 是什么"
	out := ExpandMultiQueries(q, 3)
	if len(out) < 2 {
		t.Fatalf("want ≥2 variants, got %v", out)
	}
	if out[0] != q && RewriteQuery(q) != out[0] {
		// first should be original or rewritten original
		found := false
		for _, v := range out {
			if v == q || v == RewriteQuery(q) {
				found = true
			}
		}
		if !found {
			t.Fatalf("original missing: %v", out)
		}
	}
}

func TestHydePassageIsDeterministic(t *testing.T) {
	a := HydePassage("WAL 是什么")
	b := HydePassage("WAL 是什么")
	if a == "" || a != b {
		t.Fatalf("hyde passage unstable: %q vs %q", a, b)
	}
	if !strings.Contains(a, "WAL") {
		t.Fatalf("hyde should keep topic: %q", a)
	}
}

func TestFuseHitsByRRF(t *testing.T) {
	a := []RetrievalHit{
		{ChunkID: "c/d/00000001", Score: 0.9},
		{ChunkID: "c/d/00000002", Score: 0.8},
	}
	b := []RetrievalHit{
		{ChunkID: "c/d/00000002", Score: 0.95},
		{ChunkID: "c/d/00000003", Score: 0.7},
	}
	out := FuseHitsByRRF([][]RetrievalHit{a, b}, 3)
	if len(out) == 0 {
		t.Fatal("empty fusion")
	}
	// 00000002 appears in both lists → should rank high
	if out[0].ChunkID != "c/d/00000002" {
		t.Fatalf("want 00000002 first via RRF, got %+v", out)
	}
}

func TestRetrieverMultiQueryFuses(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	emb := NewHashEmbedder(16)
	idx := NewSideIndex(16)
	// seed two chunks
	meta := &DocumentMeta{DocID: "d1", Col: "c", Title: "t", Source: "inline", ContentHash: "h", ChunkerVersion: ChunkerVersion, ChunkCount: 2}
	chunks := []ChunkRecord{
		{ChunkID: "c/d1/00000000", Col: "c", DocID: "d1", Seq: 0, Text: "TitanKV LSM storage engine"},
		{ChunkID: "c/d1/00000001", Col: "c", DocID: "d1", Seq: 1, Text: "unrelated cooking recipe"},
	}
	_ = store.SaveDocument(meta, chunks, TaskSuccess)
	for _, ch := range chunks {
		v, _ := emb.Embed(context.Background(), ch.Text)
		idx.Add(ch.ChunkID, v)
	}
	r := NewRetrieverWithConfig(emb, idx, store, NewReranker(false), RetrieverConfig{
		TopK: 2, EnableMultiQuery: true, MultiQueryN: 3,
	})
	hits, err := r.Retrieve(context.Background(), "c", "TitanKV storage", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits")
	}
}
