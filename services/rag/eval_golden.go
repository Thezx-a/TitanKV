package rag

import (
	"encoding/json"
	"fmt"
	"os"
)

// ---- Golden dataset (M0) ----
//
// testdata/golden.json 描述固定 doc_id 的语料与标注查询, 用于:
//   - make rag-eval 的回归基线 (Recall@K / MRR)
//   - M4 的 faithfulness 评测 (expected_points)
// chunk ID 布局 "col/docID/seq" 与 chunkID() 一致; 语料刻意控制在单块,
// 保证 relevant ID 在 heuristic 切分下确定可复现.

// GoldenDoc is one corpus document with a fixed doc_id.
type GoldenDoc struct {
	DocID string `json:"doc_id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// GoldenQuery is one labeled query. RelevantIDs must exist after ingest.
// ExpectedPoints are key facts the final answer should mention (M4 faithfulness).
type GoldenQuery struct {
	Query          string   `json:"query"`
	RelevantIDs    []string `json:"relevant"`
	ExpectedPoints []string `json:"expected_points,omitempty"`
}

// GoldenSet is the loaded golden dataset.
type GoldenSet struct {
	Collection  string        `json:"collection"`
	Description string        `json:"description"`
	Documents   []GoldenDoc   `json:"documents"`
	Queries     []GoldenQuery `json:"queries"`
}

// LoadGoldenSet reads and validates a golden dataset JSON file.
func LoadGoldenSet(path string) (*GoldenSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read golden set: %w", err)
	}
	var gs GoldenSet
	if err := json.Unmarshal(raw, &gs); err != nil {
		return nil, fmt.Errorf("parse golden set %s: %w", path, err)
	}
	if gs.Collection == "" || len(gs.Documents) == 0 || len(gs.Queries) == 0 {
		return nil, fmt.Errorf("golden set %s: collection/documents/queries must be non-empty", path)
	}
	for i, q := range gs.Queries {
		if q.Query == "" || len(q.RelevantIDs) == 0 {
			return nil, fmt.Errorf("golden set %s: query[%d] missing query or relevant", path, i)
		}
	}
	return &gs, nil
}

// ToEvalQueries converts golden queries to eval input.
func (gs *GoldenSet) ToEvalQueries() []EvalQuery {
	out := make([]EvalQuery, 0, len(gs.Queries))
	for _, q := range gs.Queries {
		out = append(out, EvalQuery{Query: q.Query, RelevantIDs: q.RelevantIDs})
	}
	return out
}

// ToFaithQueries converts golden queries to faithfulness eval input (M4).
func (gs *GoldenSet) ToFaithQueries() []EvalQuery {
	out := make([]EvalQuery, 0, len(gs.Queries))
	for _, q := range gs.Queries {
		out = append(out, EvalQuery{
			Query: q.Query, RelevantIDs: q.RelevantIDs, ExpectedPoints: q.ExpectedPoints,
		})
	}
	return out
}
