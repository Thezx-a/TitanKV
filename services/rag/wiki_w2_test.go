package rag

import (
	"context"
	"strings"
	"testing"
)

func TestWikiGraphDepthTwo(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	w := NewWikiStore(store)
	pages := []WikiPage{
		{Frontmatter: WikiFrontmatter{Title: "A", Slug: "a", Sources: []string{"d1"}}, Body: "see [[b]]", Summary: "A"},
		{Frontmatter: WikiFrontmatter{Title: "B", Slug: "b", Sources: []string{"d1"}}, Body: "see [[c]]", Summary: "B"},
		{Frontmatter: WikiFrontmatter{Title: "C", Slug: "c", Sources: []string{"d1"}}, Body: "leaf", Summary: "C"},
	}
	edges := []WikiEdge{
		{From: "a", To: "b", Rel: "links_to"},
		{From: "b", To: "c", Rel: "links_to"},
	}
	if err := w.SavePagesAndEdges("demo", pages, edges); err != nil {
		t.Fatal(err)
	}
	g, err := BuildWikiGraph(w, "demo", "a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if g.Slug != "a" || g.Depth != 2 {
		t.Fatalf("got %+v", g)
	}
	if len(g.Out) != 1 || g.Out[0].To != "b" {
		t.Fatalf("out=%+v", g.Out)
	}
	if g.Neighbors == nil || len(g.Neighbors["b"]) != 1 || g.Neighbors["b"][0].To != "c" {
		t.Fatalf("neighbors=%+v", g.Neighbors)
	}
}

func TestCompileContestedDoesNotOverwrite(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	emb := NewHashEmbedder(8)
	idx := NewVectorIndex(8, "brute")
	wiki := NewWikiStore(store)
	c := NewCompiler(wiki, store, emb, idx, nil, false)

	// doc-1 creates page "lsm-tree"
	chunks1 := []ChunkRecord{{
		ChunkID: "demo/doc-1/00000000", Col: "demo", DocID: "doc-1", Seq: 0,
		Heading: "LSM Tree", Text: "LSM stores sorted runs on disk.",
	}}
	_ = store.SaveDocument(&DocumentMeta{DocID: "doc-1", Col: "demo", Title: "LSM", ChunkCount: 1}, chunks1, TaskSuccess)
	if _, err := c.CompileDocument(context.Background(), "demo", "doc-1"); err != nil {
		t.Fatal(err)
	}
	orig, err := wiki.GetPage("demo", "lsm-tree")
	if err != nil || orig == nil {
		t.Fatalf("orig: %v %+v", err, orig)
	}
	origBody := orig.Body

	// doc-2 same slug, conflicting body
	chunks2 := []ChunkRecord{{
		ChunkID: "demo/doc-2/00000000", Col: "demo", DocID: "doc-2", Seq: 0,
		Heading: "LSM Tree", Text: "LSM is a B-tree alternative that NEVER uses disk runs.",
	}}
	_ = store.SaveDocument(&DocumentMeta{DocID: "doc-2", Col: "demo", Title: "LSM alt", ChunkCount: 1}, chunks2, TaskSuccess)
	if _, err := c.CompileDocument(context.Background(), "demo", "doc-2"); err != nil {
		t.Fatal(err)
	}

	// original must not be overwritten
	still, err := wiki.GetPage("demo", "lsm-tree")
	if err != nil || still == nil {
		t.Fatal("original page missing")
	}
	if still.Body != origBody {
		t.Fatalf("original overwritten: %q vs %q", still.Body, origBody)
	}
	if !still.Frontmatter.Contested {
		t.Fatal("original should be contested")
	}

	contested, err := wiki.ListContested("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(contested) < 2 {
		t.Fatalf("want >=2 contested pages, got %d %+v", len(contested), contested)
	}
	foundAlt := false
	for _, p := range contested {
		if p.Frontmatter.Slug != "lsm-tree" && strings.HasPrefix(p.Frontmatter.Slug, "lsm-tree") {
			foundAlt = true
			if !p.Frontmatter.Contested {
				t.Fatal("alt should be contested")
			}
			if !containsString(p.Frontmatter.Sources, "doc-2") {
				t.Fatalf("alt sources: %+v", p.Frontmatter.Sources)
			}
		}
	}
	if !foundAlt {
		t.Fatalf("missing alt contested page: %+v", contested)
	}

	// contradicts edge should exist
	edges, _ := wiki.ListEdges("demo", "lsm-tree", true)
	hasContra := false
	for _, e := range edges {
		if e.Rel == "contradicts" {
			hasContra = true
		}
	}
	if !hasContra {
		t.Fatalf("missing contradicts edge: %+v", edges)
	}
}
