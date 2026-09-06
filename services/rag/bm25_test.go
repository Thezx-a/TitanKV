package rag

import (
	"context"
	"strings"
	"testing"
)

// ---- M1: BM25 稀疏通道 ----

func TestTokenizeBM25(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Hello World", []string{"hello", "world"}},
		{"single x word", []string{"single", "word"}}, // 单字母 ASCII 丢弃
		{"宕机", []string{"宕机"}},
		{"为什么宕机后数据不会丢", []string{"为什", "什么", "么宕", "宕机", "机后", "后数", "数据", "据不", "不会", "会丢"}},
		{"Bloom Filter 布隆过滤器", []string{"bloom", "filter", "布隆", "隆过", "过滤", "滤器"}},
		{"401 与 512", []string{"401", "与", "512"}},
		{"  ", nil},
	}
	for _, c := range cases {
		got := tokenizeBM25(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("tokenize(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// newBM25TestStore builds a MemKV-backed store with three keyword-distinct docs.
func newBM25TestStore(t *testing.T) (*Store, *Ingester, *BM25Index) {
	t.Helper()
	store := NewStoreFromKV(NewMemKV())
	emb := NewHashEmbedder(64)
	idx := NewVectorIndexWithParams(64, "brute", HNSWParams{})
	ing := NewIngester(store, NewChunker(512, 64), emb, idx, Config{IndexDir: t.TempDir(), AsyncIngest: false})
	bm := NewBM25Index(store)
	ing.SetBM25(bm)
	ctx := context.Background()
	docs := []struct {
		id, text string
	}{
		{"d-bloom", "布隆过滤器通过位数组与多个哈希函数判断元素是否存在, 误判率随填充上升."},
		{"d-wal", "WAL 预写日志先顺序追加再修改内存, 宕机后重放日志恢复数据."},
		{"d-lsm", "LSM Tree 写入先进 MemTable 再刷成 SSTable, Compaction 合并层级."},
	}
	for _, d := range docs {
		if _, err := ing.Ingest(ctx, "c1", d.id, d.id, "inline", d.text); err != nil {
			t.Fatalf("ingest %s: %v", d.id, err)
		}
	}
	return store, ing, bm
}

func TestBM25SearchRanksKeywordDoc(t *testing.T) {
	_, _, bm := newBM25TestStore(t)
	terms, chunks := bm.Stats("c1")
	if chunks != 3 || terms == 0 {
		t.Fatalf("stats: terms=%d chunks=%d", terms, chunks)
	}
	hits := bm.Search("c1", "布隆过滤器 位数组", 3)
	if len(hits) == 0 || !strings.HasPrefix(hits[0].ChunkID, "c1/d-bloom/") {
		t.Fatalf("expected d-bloom first, got %+v", hits)
	}
	// 罕见词的 IDF 加权: "位数组" 只在 bloom 文档
	hits = bm.Search("c1", "位数组", 1)
	if len(hits) == 0 || !strings.HasPrefix(hits[0].ChunkID, "c1/d-bloom/") {
		t.Fatalf("expected d-bloom for rare term, got %+v", hits)
	}
}

func TestBM25ChineseParaphrase(t *testing.T) {
	// dense 词袋 hash embedding 覆盖不了的改述查询: "为什么宕机后数据不会丢"
	// tokenizeBM25 产出 bigram "宕机" → 命中 d-wal
	_, _, bm := newBM25TestStore(t)
	hits := bm.Search("c1", "为什么宕机后数据不会丢", 3)
	if len(hits) == 0 || !strings.HasPrefix(hits[0].ChunkID, "c1/d-wal/") {
		t.Fatalf("expected d-wal via CJK bigram, got %+v", hits)
	}
}

func TestBM25RemoveDoc(t *testing.T) {
	_, _, bm := newBM25TestStore(t)
	if err := bm.RemoveDoc("c1", "d-bloom"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if hits := bm.Search("c1", "布隆过滤器", 3); len(hits) > 0 {
		for _, h := range hits {
			if strings.HasPrefix(h.ChunkID, "c1/d-bloom/") {
				t.Fatalf("d-bloom should be removed, got %+v", h)
			}
		}
	}
	_, chunks := bm.Stats("c1")
	if chunks != 2 {
		t.Fatalf("chunk count after remove = %d, want 2", chunks)
	}
}

func TestBM25PersistenceRoundtrip(t *testing.T) {
	store, _, bm := newBM25TestStore(t)
	before := bm.Search("c1", "布隆过滤器 位数组", 3)

	// 模拟重启: 新 index 从 minikv 恢复
	bm2 := NewBM25Index(store)
	n, err := bm2.LoadFromStore()
	if err != nil || n != 1 {
		t.Fatalf("load: n=%d err=%v", n, err)
	}
	after := bm2.Search("c1", "布隆过滤器 位数组", 3)
	if len(after) != len(before) {
		t.Fatalf("roundtrip length mismatch: %d vs %d", len(after), len(before))
	}
	for i := range before {
		if after[i].ChunkID != before[i].ChunkID || after[i].Score != before[i].Score {
			t.Fatalf("roundtrip mismatch at %d: %+v vs %+v", i, after[i], before[i])
		}
	}
}

func TestBM25RemoveDocPersisted(t *testing.T) {
	// 删除 → 重启恢复后删除依然生效
	store, _, bm := newBM25TestStore(t)
	if err := bm.RemoveDoc("c1", "d-wal"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	bm2 := NewBM25Index(store)
	if _, err := bm2.LoadFromStore(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, chunks := bm2.Stats("c1"); chunks != 2 {
		t.Fatalf("chunks after restart = %d, want 2", chunks)
	}
}

func TestBM25RebuildFromChunks(t *testing.T) {
	// 自愈: posting 丢失 (pre-M1 遗留数据) → 从 rag:chunk:* 重建
	store, _, _ := newBM25TestStore(t)
	// 清空内存 + 持久层 posting, 但保留 chunk 原文
	if err := store.DeletePrefix("rag:bm25:"); err != nil {
		t.Fatalf("wipe postings: %v", err)
	}
	bm3 := NewBM25Index(store)
	bm3.MaybeRebuildFromChunks()
	if hits := bm3.Search("c1", "布隆过滤器 位数组", 3); len(hits) == 0 ||
		!strings.HasPrefix(hits[0].ChunkID, "c1/d-bloom/") {
		t.Fatalf("rebuild failed to restore search, got %+v", hits)
	}
}

func TestBM25IdempotentAdd(t *testing.T) {
	// 同文档重复 Add 不产生重复计数 (幂等)
	_, ing, bm := newBM25TestStore(t)
	ctx := context.Background()
	if _, err := ing.Ingest(ctx, "c1", "d-bloom", "d-bloom", "inline",
		"布隆过滤器通过位数组与多个哈希函数判断元素是否存在, 误判率随填充上升."); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	_, chunks := bm.Stats("c1")
	if chunks != 3 {
		t.Fatalf("chunks after idempotent re-add = %d, want 3", chunks)
	}
}

func TestBM25RRFFusionInRetriever(t *testing.T) {
	// 端到端: Retriever 挂上 BM25 后, 改述中文查询从 0 命中变可命中
	store := NewStoreFromKV(NewMemKV())
	emb := NewHashEmbedder(64)
	idx := NewVectorIndexWithParams(64, "brute", HNSWParams{})
	cfg := Config{IndexDir: t.TempDir(), AsyncIngest: false}
	ing := NewIngester(store, NewChunker(512, 64), emb, idx, cfg)
	bm := NewBM25Index(store)
	ing.SetBM25(bm)
	ctx := context.Background()
	if _, err := ing.Ingest(ctx, "c1", "d-wal", "d-wal", "inline",
		"WAL 预写日志先顺序追加再修改内存, 宕机后重放日志恢复数据."); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	retDense := NewRetrieverWithConfig(emb, idx, store, NewReranker(false), RetrieverConfig{TopK: 5})
	retHybrid := NewRetrieverWithConfig(emb, idx, store, NewReranker(false), RetrieverConfig{TopK: 5})
	retHybrid.SetBM25(bm)

	q := "为什么宕机后数据不会丢"
	denseHits, _ := retDense.Retrieve(ctx, "c1", q, 5)
	hybridHits, err := retHybrid.Retrieve(ctx, "c1", q, 5)
	if err != nil {
		t.Fatalf("hybrid retrieve: %v", err)
	}
	if len(hybridHits) == 0 {
		t.Fatal("hybrid should find d-wal via BM25 lane")
	}
	if len(denseHits) > 0 && strings.HasPrefix(denseHits[0].ChunkID, "c1/d-wal/") {
		t.Log("dense also hit (unexpected but not fatal)")
	}
	if !strings.HasPrefix(hybridHits[0].ChunkID, "c1/d-wal/") {
		t.Fatalf("hybrid top1 should be d-wal, got %+v", hybridHits[0])
	}
}
