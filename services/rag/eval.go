package rag

import (
	"context"
	"math"
)

// EvalResult holds retrieval quality metrics for a fixed query set.
type EvalResult struct {
	Queries   int     `json:"queries"`
	RecallAtK float64 `json:"recall_at_k"`
	MRR       float64 `json:"mrr"`
	TopK      int     `json:"top_k"`
}

// EvalQuery is one labeled query with relevant chunk IDs.
type EvalQuery struct {
	Query       string
	RelevantIDs []string
}

// Evaluate runs labeled queries through the retriever and computes Recall@K and MRR.
func Evaluate(ctx context.Context, ret *Retriever, col string, queries []EvalQuery, topK int) EvalResult {
	if topK <= 0 {
		topK = 5
	}
	var recallSum, mrrSum float64
	for _, q := range queries {
		hits, err := ret.Retrieve(ctx, col, q.Query, topK)
		if err != nil || len(hits) == 0 {
			continue
		}
		rel := make(map[string]struct{}, len(q.RelevantIDs))
		for _, id := range q.RelevantIDs {
			rel[id] = struct{}{}
		}
		var hitCount int
		for i, h := range hits {
			if _, ok := rel[h.ChunkID]; ok {
				hitCount++
				if mrrSum == 0 || float64(i+1) < math.MaxFloat64 {
					mrrSum += 1.0 / float64(i+1)
				}
			}
		}
		if len(rel) > 0 {
			recallSum += float64(hitCount) / float64(len(rel))
		}
	}
	n := float64(len(queries))
	if n == 0 {
		return EvalResult{TopK: topK}
	}
	return EvalResult{
		Queries:   len(queries),
		RecallAtK: recallSum / n,
		MRR:       mrrSum / n,
		TopK:      topK,
	}
}
