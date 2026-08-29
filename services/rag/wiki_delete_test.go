package rag

import (
	"context"
	"testing"
)

func TestDeleteDocumentClearsWiki(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	emb := NewHashEmbedder(8)
	idx := NewVectorIndex(8, "brute")
	wiki := NewWikiStore(store)
	c := NewCompiler(wiki, store, emb, idx, nil, false)

	chunks := []ChunkRecord{{
		ChunkID: "demo/doc-x/00000000", Col: "demo", DocID: "doc-x", Seq: 0,
		Heading: "Alpha Page", Text: "hello [[Beta Page]]",
	}, {
		ChunkID: "demo/doc-x/00000001", Col: "demo", DocID: "doc-x", Seq: 1,
		Heading: "Beta Page", Text: "world",
	}}
	_ = store.SaveDocument(&DocumentMeta{DocID: "doc-x", Col: "demo", Title: "Alpha", ChunkCount: 2}, chunks, TaskSuccess)
	if _, err := c.CompileDocument(context.Background(), "demo", "doc-x"); err != nil {
		t.Fatal(err)
	}
	if p, _ := wiki.GetPage("demo", "alpha-page"); p == nil {
		t.Fatal("expected alpha-page")
	}
	if idx.Size() < 1 {
		t.Fatal("expected wiki vectors")
	}

	// simulate DeleteDocument wiki cleanup
	if ix, err := wiki.LoadIndex("demo"); err == nil {
		for _, e := range ix.Entries {
			if p, _ := wiki.GetPage("demo", e.Slug); p != nil && containsString(p.Frontmatter.Sources, "doc-x") {
				idx.Delete(wikiChunkID("demo", e.Slug))
			}
		}
	}
	_ = wiki.DeleteBySource("demo", "doc-x")
	_ = store.DeleteDocument("demo", "doc-x")
	idx.ClearByPrefix("demo/doc-x/")

	if p, _ := wiki.GetPage("demo", "alpha-page"); p != nil {
		t.Fatal("wiki page should be gone")
	}
	if p, _ := wiki.GetPage("demo", "beta-page"); p != nil {
		t.Fatal("beta-page should be gone")
	}
}
