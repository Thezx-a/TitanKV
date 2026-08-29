package rag

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCompileDocumentCreatesPagesAndIndex(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	// seed rag chunks as if ingest already succeeded
	chunks := []ChunkRecord{
		{ChunkID: "demo/doc-1/00000000", Col: "demo", DocID: "doc-1", Seq: 0,
			Heading: "TitanKV Overview", Text: "TitanKV is a KV store. See [[LSM Tree]]."},
		{ChunkID: "demo/doc-1/00000001", Col: "demo", DocID: "doc-1", Seq: 1,
			Heading: "LSM Tree", Text: "Log-structured merge tree."},
	}
	meta := &DocumentMeta{DocID: "doc-1", Col: "demo", Title: "TitanKV Overview", Source: "inline", ChunkCount: 2}
	if err := store.SaveDocument(meta, chunks, TaskSuccess); err != nil {
		t.Fatal(err)
	}

	emb := NewHashEmbedder(16)
	idx := NewVectorIndex(16, "brute")
	c := NewCompiler(NewWikiStore(store), store, emb, idx, nil, false) // LLM off → rule summary
	task, err := c.CompileDocument(context.Background(), "demo", "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskSuccess || task.Pages < 1 {
		t.Fatalf("task=%+v", task)
	}
	w := NewWikiStore(store)
	p, err := w.GetPage("demo", "titankv-overview")
	if err != nil || p == nil {
		t.Fatalf("page: %v %+v", err, p)
	}
	if p.Summary == "" {
		t.Fatal("summary empty")
	}
	// wiki summary should be in side index
	if idx.Size() < 1 {
		t.Fatal("expected wiki vectors in index")
	}
}

func TestWikiFirstRetrievePrefersExactSlug(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	w := NewWikiStore(store)
	_ = w.SavePagesAndEdges("demo", []WikiPage{{
		Frontmatter: WikiFrontmatter{Title: "LSM Tree", Slug: "lsm-tree", Type: WikiPageConcept, Sources: []string{"d1"}},
		Body:        "Sorted runs on disk.",
		Summary:     "LSM stores sorted runs",
	}}, nil)

	emb := NewHashEmbedder(16)
	idx := NewVectorIndex(16, "brute")
	ret := NewRetrieverOpts(emb, idx, store, NewReranker(false), 5, false)
	wq := NewWikiQuerier(w)

	hits, fallback, err := WikiFirstRetrieve(context.Background(), "demo", "LSM Tree", 3, wq, ret)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Slug != "lsm-tree" || hits[0].Source != "wiki" {
		t.Fatalf("hits=%+v fallback=%+v", hits, fallback)
	}
}

func TestCompileEnqueueAsync(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	chunks := []ChunkRecord{{
		ChunkID: "c/d/00000000", Col: "c", DocID: "d", Seq: 0,
		Heading: "Hello", Text: "world",
	}}
	_ = store.SaveDocument(&DocumentMeta{DocID: "d", Col: "c", Title: "Hello", ChunkCount: 1}, chunks, TaskSuccess)

	emb := NewHashEmbedder(8)
	idx := NewVectorIndex(8, "brute")
	c := NewCompiler(NewWikiStore(store), store, emb, idx, nil, false)
	pool := NewCompilePool(CompilePoolConfig{Workers: 1, QueueSize: 8}, c)
	defer pool.Close()

	taskID, err := pool.Enqueue("c", "d")
	if err != nil || taskID == "" {
		t.Fatalf("enqueue: %v %s", err, taskID)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		t, e := NewWikiStore(store).LoadCompileTask(taskID)
		if e == nil && t.Status == TaskSuccess {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("compile task did not reach success")
}

func TestAssembleWikiPromptUsesWikiHits(t *testing.T) {
	p := assembleWikiPrompt("什么是 LSM?", []WikiHit{{
		Slug: "lsm-tree", Title: "LSM Tree", BodyExcerpt: "Sorted runs", Source: "wiki",
	}}, nil, nil)
	if !strings.Contains(p, "lsm-tree") || !strings.Contains(p, "Sorted runs") {
		t.Fatalf("prompt=%s", p)
	}
}
