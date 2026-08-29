package rag

import "testing"

func TestDeletePrefixAndWikiCollection(t *testing.T) {
	store := NewStoreFromKV(NewMemKV())
	w := NewWikiStore(store)

	_ = store.Put("wiki:page:demo:a", "1")
	_ = store.Put("wiki:page:demo:b", "2")
	_ = store.Put("wiki:edge:demo:a:b", "e")
	_ = store.Put("wiki:raw:demo:doc1", "raw")
	_ = store.PutJSON(wikiIndexKey("demo"), WikiIndexDoc{Col: "demo"})
	_ = store.Put("wiki:page:other:x", "keep")
	_ = store.Put("rag:chunk:demo:d:00000000", "c")
	_ = store.Put("keep:me", "alive")

	if err := w.DeleteCollection("demo"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get("wiki:page:demo:a"); ok {
		t.Fatal("page should be gone")
	}
	if _, ok, _ := store.Get("wiki:edge:demo:a:b"); ok {
		t.Fatal("edge should be gone")
	}
	if _, ok, _ := store.Get("wiki:index:demo"); ok {
		t.Fatal("index should be gone")
	}
	if _, ok, _ := store.Get("wiki:page:other:x"); !ok {
		t.Fatal("other collection must survive")
	}

	if err := store.DeleteCollection("demo"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Get("rag:chunk:demo:d:00000000"); ok {
		t.Fatal("rag chunk should be gone")
	}
	if _, ok, _ := store.Get("keep:me"); !ok {
		t.Fatal("unrelated key must survive")
	}
}
