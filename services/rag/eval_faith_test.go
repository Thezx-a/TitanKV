package rag

import (
	"context"
	"strings"
	"testing"
)

// ---- M4: FaithfulnessScore 规则评分 ----

func TestFaithfulnessSupportedAnswer(t *testing.T) {
	ctxs := []string{
		"LSM-Tree 写入先进 MemTable 内存跳表, 达到阈值后刷成 SSTable 落盘, 后台 Compaction 合并。",
	}
	ans := "写入先进 MemTable, 然后刷成 SSTable 落盘。Compaction 在后台合并。"
	score, unsupported := FaithfulnessScore(ans, ctxs)
	if score != 1 {
		t.Fatalf("score = %v, want 1 (unsupported=%v)", score, unsupported)
	}
}

func TestFaithfulnessHallucinatedAnswer(t *testing.T) {
	// 故意注入引用中不存在的断言 (坏答案) → 评分必须下降
	ctxs := []string{
		"LSM-Tree 写入先进 MemTable 内存跳表, 达到阈值后刷成 SSTable 落盘。",
	}
	ans := "LSM-Tree 写入先进 MemTable。它使用 B+树存储并用 Raft 协议同步到三个可用区。"
	score, unsupported := FaithfulnessScore(ans, ctxs)
	if score >= 1 {
		t.Fatalf("hallucinated answer scored %v, want < 1", score)
	}
	if len(unsupported) == 0 {
		t.Fatal("unsupported sentences not reported")
	}
	found := false
	for _, s := range unsupported {
		if strings.Contains(s, "Raft") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Raft sentence should be flagged: %v", unsupported)
	}
}

func TestFaithfulnessEmptyAndNeutral(t *testing.T) {
	// 空答案不扣分 (上层处理), 纯标点/停用词句子中性
	if s, _ := FaithfulnessScore("", []string{"x"}); s != 1 {
		t.Fatalf("empty answer score = %v", s)
	}
	if s, _ := FaithfulnessScore("的 了 和 。", []string{"x"}); s != 1 {
		t.Fatalf("neutral answer score = %v", s)
	}
}

// ---- KeyPointRecall ----

func TestKeyPointRecall(t *testing.T) {
	hits := []RetrievalHit{
		{Text: "LSM-Tree 写入先进 MemTable, 达到阈值刷成 SSTable 落盘。"},
		{Text: "后台 Compaction 合并多层 SSTable 降低读放大。"},
	}
	if r := KeyPointRecall([]string{"MemTable", "SSTable", "Compaction"}, hits); r != 1 {
		t.Fatalf("recall = %v, want 1", r)
	}
	if r := KeyPointRecall([]string{"MemTable", "Bloom Filter", "MVCC"}, hits); r != 1.0/3.0 {
		t.Fatalf("recall = %v, want 0.333", r)
	}
	if r := KeyPointRecall(nil, hits); r != 1 {
		t.Fatalf("no points should score 1, got %v", r)
	}
}

// ---- CI 门禁: 坏答案能被拦 (验收要求) ----

func TestFaithGateBlocksHallucination(t *testing.T) {
	// 模拟 CI: 参照答案 vs 幻觉答案, 门禁只放行前者
	ctxs := []string{"布隆过滤器用位数组和多个哈希函数判断元素存在, 空间效率高。"}

	goodScore, _ := FaithfulnessScore("布隆过滤器用位数组和哈希函数判断元素存在。", ctxs)
	badScore, _ := FaithfulnessScore("布隆过滤器使用跳表和 LSM 合并, 并通过 Raft 保持线性一致。", ctxs)

	if goodScore < faithGateFloor {
		t.Fatalf("good answer rejected: %v", goodScore)
	}
	if badScore >= faithGateFloor {
		t.Fatalf("hallucinated answer passed gate: %v", badScore)
	}
}

const faithGateFloor = 0.99 // 提取式/忠实答案必须满分过门禁

func TestEvaluateFaithPipeline(t *testing.T) {
	store, ing, ret, gs := newGoldenHarness(t, true)
	ingestGolden(t, ing, store, gs)
	res, err := EvaluateFaith(context.Background(), ret, gs.Collection, gs.ToFaithQueries(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Queries != len(gs.Queries) {
		t.Fatalf("queries = %d, want %d", res.Queries, len(gs.Queries))
	}
	if res.Faithfulness != 1 {
		t.Fatalf("extractive sanity broken: %v (unsupported=%d)", res.Faithfulness, res.UnsupportedN)
	}
	if res.KeyPointRecall < 0.95 {
		t.Fatalf("key point recall = %v", res.KeyPointRecall)
	}
}
