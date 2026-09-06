package rag

import (
	"context"
	"strings"
	"testing"
)

// newWikiVerStore 构造带两页 wiki 的 MemKV store.
func newWikiVerStore(t *testing.T) (*WikiStore, *Store) {
	t.Helper()
	store := NewStoreFromKV(NewMemKV())
	w := NewWikiStore(store)
	pages := []WikiPage{
		{Frontmatter: WikiFrontmatter{Title: "LSM Tree", Slug: "lsm-tree", Type: WikiPageEntity, Sources: []string{"doc-lsm"}}, Body: "v1: LSM-Tree 写入先进 MemTable, 刷成 SSTable 落盘。"},
		{Frontmatter: WikiFrontmatter{Title: "Bloom Filter", Slug: "bloom-filter", Type: WikiPageEntity, Sources: []string{"doc-bloom"}}, Body: "v1: 布隆过滤器用位数组判断元素存在。"},
	}
	if err := w.SavePagesAndEdges("demo", pages, nil); err != nil {
		t.Fatal(err)
	}
	return w, store
}

func TestWikiVersionArchiveOnSave(t *testing.T) {
	w, _ := newWikiVerStore(t)
	// 第二次 compile: 覆盖 lsm-tree → v1 应被归档
	pages := []WikiPage{
		{Frontmatter: WikiFrontmatter{Title: "LSM Tree", Slug: "lsm-tree", Type: WikiPageEntity, Sources: []string{"doc-lsm"}}, Body: "v2: 重写后的内容。"},
	}
	if err := w.SavePagesAndEdges("demo", pages, nil); err != nil {
		t.Fatal(err)
	}
	vers, err := w.ListPageVersions("demo", "lsm-tree")
	if err != nil || len(vers) != 1 || vers[0] != 1 {
		t.Fatalf("versions = %v err=%v, want [1]", vers, err)
	}
	// 未覆盖的 bloom-filter 无归档
	if vers, _ := w.ListPageVersions("demo", "bloom-filter"); len(vers) != 0 {
		t.Fatalf("bloom-filter should have no versions, got %v", vers)
	}
}

func TestWikiKeepVersionsPrune(t *testing.T) {
	w, _ := newWikiVerStore(t)
	w.SetKeepVersions(2)
	// 连续覆盖 4 次 → 只保留最近 2 份归档
	for i := 2; i <= 5; i++ {
		pages := []WikiPage{
			{Frontmatter: WikiFrontmatter{Title: "LSM Tree", Slug: "lsm-tree", Type: WikiPageEntity, Sources: []string{"doc-lsm"}},
				Body: "v" + string(rune('0'+i)) + " content"},
		}
		if err := w.SavePagesAndEdges("demo", pages, nil); err != nil {
			t.Fatal(err)
		}
	}
	vers, _ := w.ListPageVersions("demo", "lsm-tree")
	if len(vers) != 2 || vers[0] != 3 || vers[1] != 4 {
		t.Fatalf("pruned versions = %v, want [3 4]", vers)
	}
	// 回滚到被淘汰的版本必须报错
	if _, err := w.RollbackPage("demo", "lsm-tree", 1); err == nil {
		t.Fatal("rollback to pruned version should fail")
	}
	// 回滚到保留的版本
	p, err := w.RollbackPage("demo", "lsm-tree", 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Body, "v4") {
		t.Fatalf("restored body = %q, want v4 content", p.Body)
	}
}

func TestWikiRollback(t *testing.T) {
	w, _ := newWikiVerStore(t)
	// 覆盖 (模拟坏 compile: 篡改内容)
	bad := []WikiPage{
		{Frontmatter: WikiFrontmatter{Title: "LSM Tree", Slug: "lsm-tree", Type: WikiPageEntity, Sources: []string{"doc-lsm"}}, Body: "被幻觉污染的内容, 包含 Raft 三副本同步。"},
	}
	if err := w.SavePagesAndEdges("demo", bad, nil); err != nil {
		t.Fatal(err)
	}
	cur, _ := w.GetPage("demo", "lsm-tree")
	if !strings.Contains(cur.Body, "幻觉") {
		t.Fatalf("setup: bad page not live: %q", cur.Body)
	}
	// 回滚到上一版
	p, err := w.RollbackPage("demo", "lsm-tree", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Body, "v1:") || strings.Contains(p.Body, "幻觉") {
		t.Fatalf("rollback did not restore v1: %q", p.Body)
	}
	// live 页已恢复
	live, _ := w.GetPage("demo", "lsm-tree")
	if !strings.Contains(live.Body, "v1:") {
		t.Fatalf("live page not restored: %q", live.Body)
	}
	// 索引条目仍在
	idx, _ := w.LoadIndex("demo")
	found := false
	for _, e := range idx.Entries {
		if e.Slug == "lsm-tree" {
			found = true
		}
	}
	if !found {
		t.Fatal("index entry lost after rollback")
	}
}

func TestWikiRollbackNoVersions(t *testing.T) {
	w, _ := newWikiVerStore(t)
	if _, err := w.RollbackPage("demo", "bloom-filter", 0); err == nil {
		t.Fatal("rollback without versions should fail")
	}
}

// ---- 引用支撑度审计 ----

func newWikiAuditFixture(t *testing.T) (*Store, *WikiStore) {
	t.Helper()
	store := NewStoreFromKV(NewMemKV())
	// source chunks: doc-lsm 有真实正文
	if err := store.PutJSON(chunkKey("demo", "doc-lsm", 0), ChunkRecord{
		Col: "demo", DocID: "doc-lsm", Seq: 0,
		Text: "LSM-Tree 写入先进 MemTable 内存跳表, 达到阈值后不可变刷成 SSTable 落盘, 后台 Compaction 合并多层。",
	}); err != nil {
		t.Fatal(err)
	}
	w := NewWikiStore(store)
	pages := []WikiPage{
		// 支撑良好: 正文全部来自 source
		{Frontmatter: WikiFrontmatter{Title: "LSM Tree", Slug: "lsm-tree", Sources: []string{"doc-lsm"}},
			Body: "LSM-Tree 写入先进 MemTable 内存跳表。达到阈值后刷成 SSTable 落盘。"},
		// 支撑不足: 引入了 source 中不存在的断言
		{Frontmatter: WikiFrontmatter{Title: "LSM Tree Bad", Slug: "lsm-tree-bad", Sources: []string{"doc-lsm"}},
			Body: "LSM-Tree 写入先进 MemTable。它使用 Raft 协议跨三个可用区同步副本, 并由 Kafka 消费写请求。"},
		// 无 source: doc 已删 → skipped
		{Frontmatter: WikiFrontmatter{Title: "Orphan", Slug: "orphan", Sources: []string{"doc-deleted"}},
			Body: "孤儿页"},
	}
	if err := w.SavePagesAndEdges("demo", pages, nil); err != nil {
		t.Fatal(err)
	}
	return store, w
}

func TestAuditWikiSources(t *testing.T) {
	store, w := newWikiAuditFixture(t)
	rep, err := AuditWikiSources(context.Background(), w, store, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pages != 3 || rep.Audited != 2 || rep.Skipped != 1 {
		t.Fatalf("counts: pages=%d audited=%d skipped=%d", rep.Pages, rep.Audited, rep.Skipped)
	}
	if rep.BelowThreshold != 1 {
		t.Fatalf("below_threshold = %d, want 1", rep.BelowThreshold)
	}
	// 低支撑页排最前
	if rep.Entries[0].Slug != "lsm-tree-bad" {
		t.Fatalf("sort order: first = %s", rep.Entries[0].Slug)
	}
	// 幻觉句子被点名
	found := false
	for _, s := range rep.Entries[0].Unsupported {
		if strings.Contains(s, "Raft") || strings.Contains(s, "Kafka") {
			found = true
		}
	}
	if !found {
		t.Fatalf("hallucinated sentences not flagged: %+v", rep.Entries[0].Unsupported)
	}
	// 良好页满分
	for _, e := range rep.Entries {
		if e.Slug == "lsm-tree" && e.Faithfulness != 1 {
			t.Fatalf("good page faithfulness = %v", e.Faithfulness)
		}
	}
}
