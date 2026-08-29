package rag

import (
	"strings"
	"testing"
)

func TestSlugifyTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"TitanKV Architecture", "titankv-architecture"},
		{"  Hello World  ", "hello-world"},
		{"中文标题 LSM", "中文标题-lsm"},
		{"A---B__C", "a-b-c"},
		{"", "untitled"},
	}
	for _, tc := range cases {
		got := SlugifyTitle(tc.in)
		if got != tc.want {
			t.Fatalf("SlugifyTitle(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestWikiKeySpace(t *testing.T) {
	if wikiPageKey("demo", "titankv") != "wiki:page:demo:titankv" {
		t.Fatal(wikiPageKey("demo", "titankv"))
	}
	if wikiEdgeKey("demo", "a", "b") != "wiki:edge:demo:a:b" {
		t.Fatal(wikiEdgeKey("demo", "a", "b"))
	}
	if wikiRawKey("demo", "doc1") != "wiki:raw:demo:doc1" {
		t.Fatal(wikiRawKey("demo", "doc1"))
	}
	if wikiIndexKey("demo") != "wiki:index:demo" {
		t.Fatal(wikiIndexKey("demo"))
	}
	if !strings.HasPrefix(wikiTaskKey("t1"), "wiki:task:") {
		t.Fatal(wikiTaskKey("t1"))
	}
}

func TestExtractMarkdownHeadingsAndLinks(t *testing.T) {
	md := `# TitanKV Overview

TitanKV is a KV store. See also [[LSM Tree]] and [[Write Path]].

## LSM Tree

Log-structured merge tree stores sorted runs.

## Write Path

Put goes WAL then MemTable. Links back to [[TitanKV Overview]].
`
	pages, edges := ExtractFromMarkdown(md, "doc-1")
	if len(pages) < 3 {
		t.Fatalf("want >=3 pages (overview+2 headings), got %d: %+v", len(pages), pageSlugs(pages))
	}
	slugs := map[string]bool{}
	for _, p := range pages {
		slugs[p.Frontmatter.Slug] = true
		if p.Frontmatter.Slug == "" {
			t.Fatal("empty slug")
		}
		if len(p.Frontmatter.Sources) == 0 || p.Frontmatter.Sources[0] != "doc-1" {
			t.Fatalf("sources: %+v", p.Frontmatter.Sources)
		}
		if !strings.Contains(p.Body, "[[") && p.Frontmatter.Slug == "titankv-overview" {
			t.Fatalf("overview body should keep wikilinks: %s", p.Body)
		}
	}
	if !slugs["titankv-overview"] || !slugs["lsm-tree"] || !slugs["write-path"] {
		t.Fatalf("missing expected slugs: %v", slugs)
	}
	if len(edges) < 2 {
		t.Fatalf("want edges from [[wikilink]], got %d", len(edges))
	}
	found := false
	for _, e := range edges {
		if e.From == "titankv-overview" && e.To == "lsm-tree" && e.Rel == "links_to" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing overview→lsm-tree edge: %+v", edges)
	}
}

func pageSlugs(pages []WikiPage) []string {
	out := make([]string, len(pages))
	for i, p := range pages {
		out[i] = p.Frontmatter.Slug
	}
	return out
}

func TestWikiStoreSaveGetDeleteBySource(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	w := NewWikiStore(store)
	col := "demo"
	docID := "doc-1"

	pages := []WikiPage{
		{
			Frontmatter: WikiFrontmatter{
				Title: "TitanKV", Slug: "titankv", Type: WikiPageEntity,
				Sources: []string{docID}, Confidence: "medium",
			},
			Body:    "KV store. See [[lsm-tree]].",
			Summary: "KV store overview",
		},
		{
			Frontmatter: WikiFrontmatter{
				Title: "LSM Tree", Slug: "lsm-tree", Type: WikiPageConcept,
				Sources: []string{docID}, Confidence: "medium",
			},
			Body:    "Sorted runs.",
			Summary: "LSM concept",
		},
	}
	edges := []WikiEdge{
		{From: "titankv", To: "lsm-tree", Rel: "links_to"},
	}

	if err := w.SaveRaw(col, docID, []byte("raw body"), "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := w.SavePagesAndEdges(col, pages, edges); err != nil {
		t.Fatal(err)
	}

	got, err := w.GetPage(col, "titankv")
	if err != nil || got == nil {
		t.Fatalf("GetPage: %v %+v", err, got)
	}
	if got.Frontmatter.Title != "TitanKV" || got.Summary != "KV store overview" {
		t.Fatalf("page mismatch: %+v", got)
	}
	outEdges, err := w.ListEdges(col, "titankv", true)
	if err != nil || len(outEdges) != 1 || outEdges[0].To != "lsm-tree" {
		t.Fatalf("ListEdges: %v %+v", err, outEdges)
	}
	idx, err := w.LoadIndex(col)
	if err != nil || idx == nil || len(idx.Entries) < 2 {
		t.Fatalf("LoadIndex: %v %+v", err, idx)
	}

	if err := w.DeleteBySource(col, docID); err != nil {
		t.Fatal(err)
	}
	if p, _ := w.GetPage(col, "titankv"); p != nil {
		t.Fatal("page should be gone after DeleteBySource")
	}
	if p, _ := w.GetPage(col, "lsm-tree"); p != nil {
		t.Fatal("lsm-tree should be gone")
	}
	if es, _ := w.ListEdges(col, "titankv", true); len(es) != 0 {
		t.Fatalf("edges should be gone: %+v", es)
	}
}
