package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ---- M0: golden dataset 回归基线 ----
//
// 端到端竖切: MemKV + hash embedder + 真实 chunker/ingester/retriever.
// 不依赖 minikv 与外部网络, 可在任何环境跑 (`make rag-eval`).
//
// 断言:
//   1. 语料全部入库成功, 且 golden 的 relevant chunk ID 全部真实存在 (数据集自校验)
//   2. Recall@K / MRR 不低于基线 (GoldenRecallFloor / GoldenMRRFloor)
//
// 基线数字记录在 docs/rag-eval-baseline.md; 新功能 (BM25 等) 只允许向上.

const (
	goldenRecallFloor = 0.83 // M0 实测基线 0.929 * 0.9
	goldenMRRFloor    = 0.75 // M0 实测基线 0.833 * 0.9
)

// newGoldenHarness 构建与 NewService 同构的检索链路 (无 wiki/池, 同步入库).
func newGoldenHarness(t *testing.T) (*Store, *Ingester, *Retriever, *GoldenSet) {
	t.Helper()
	gs, err := LoadGoldenSet(filepath.Join("testdata", "golden.json"))
	if err != nil {
		t.Fatalf("load golden set: %v", err)
	}
	store := NewStoreFromKV(NewMemKV())
	emb := NewHashEmbedder(384)
	// brute 精确检索: golden 门禁要确定性, 不接受 HNSW 近似带来的波动
	idx := NewVectorIndexWithParams(emb.Dim(), "brute", HNSWParams{})
	cfg := Config{IndexDir: t.TempDir(), AsyncIngest: false, EmbeddingBatch: 32}
	ing := NewIngester(store, NewChunker(512, 64), emb, idx, cfg)
	ret := NewRetrieverWithConfig(emb, idx, store, NewReranker(false), RetrieverConfig{TopK: 5})
	return store, ing, ret, gs
}

// ingestGolden 同步入库全部语料并断言 relevant chunk 存在 (golden 自校验).
func ingestGolden(t *testing.T, ing *Ingester, store *Store, gs *GoldenSet) {
	t.Helper()
	ctx := context.Background()
	for _, d := range gs.Documents {
		task, err := ing.Ingest(ctx, gs.Collection, d.DocID, d.Title, "inline", d.Text)
		if err != nil || task.Status != TaskSuccess {
			t.Fatalf("ingest %s failed: task=%+v err=%v", d.DocID, task, err)
		}
	}
	// golden 的 relevant ID 必须真实存在于 store (否则数据集写错)
	seen := map[string]bool{}
	for _, q := range gs.Queries {
		for _, id := range q.RelevantIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			col, docID, seq, ok := parseChunkID(id)
			if !ok {
				t.Fatalf("bad chunk id in golden set: %s", id)
			}
			if _, ok2, _ := store.Get(chunkKey(col, docID, seq)); !ok2 {
				t.Fatalf("golden relevant id %s missing after ingest (chunking drifted?)", id)
			}
		}
	}
}

// TestGoldenEvalBaseline 是 make rag-eval 的入口: 输出并断言 Recall@K / MRR.
func TestGoldenEvalBaseline(t *testing.T) {
	store, ing, ret, gs := newGoldenHarness(t)
	ingestGolden(t, ing, store, gs)

	res := Evaluate(context.Background(), ret, gs.Collection, gs.ToEvalQueries(), 5)
	fmt.Printf("[golden-eval] queries=%d topK=%d Recall@5=%.3f MRR=%.3f\n",
		res.Queries, res.TopK, res.RecallAtK, res.MRR)

	if res.RecallAtK < goldenRecallFloor {
		t.Errorf("Recall@K=%.3f below floor %.3f (quality regression, see docs/rag-eval-baseline.md)",
			res.RecallAtK, goldenRecallFloor)
	}
	if res.MRR < goldenMRRFloor {
		t.Errorf("MRR=%.3f below floor %.3f (quality regression, see docs/rag-eval-baseline.md)",
			res.MRR, goldenMRRFloor)
	}
}

// TestGoldenSetValid 校验数据集文件本身的完整性与 ID 自洽.
func TestGoldenSetValid(t *testing.T) {
	gs, err := LoadGoldenSet(filepath.Join("testdata", "golden.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(gs.Documents) < 8 || len(gs.Queries) < 24 {
		t.Errorf("golden set too small: docs=%d queries=%d", len(gs.Documents), len(gs.Queries))
	}
	docs := map[string]bool{}
	for _, d := range gs.Documents {
		if d.DocID == "" || d.Text == "" {
			t.Errorf("doc %q empty", d.DocID)
		}
		docs[d.DocID] = true
	}
	for i, q := range gs.Queries {
		for _, id := range q.RelevantIDs {
			col, docID, _, ok := parseChunkID(id)
			if !ok || col != gs.Collection || !docs[docID] {
				t.Errorf("query[%d] references unknown doc: %s", i, id)
			}
		}
	}
	_ = os.Environ() // keep os import if floors change to env-driven later
}
