package rag

import (
	"fmt"
	"sort"
	"strings"
)

// ExpandMultiQueries returns up to n query variants for multi-query fusion.
// Always includes the (rewritten) original; extra variants are rule-based (no LLM).
func ExpandMultiQueries(query string, n int) []string {
	if n <= 0 {
		n = 3
	}
	base := strings.TrimSpace(query)
	if base == "" {
		return nil
	}
	rewritten := RewriteQuery(base)
	seen := map[string]struct{}{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(base)
	add(rewritten)
	add(fmt.Sprintf("%s 原理", rewritten))
	add(fmt.Sprintf("%s 实现", rewritten))
	add(fmt.Sprintf("什么是 %s", rewritten))
	add(fmt.Sprintf("%s explanation", rewritten))
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// HydePassage builds a deterministic pseudo-document for HyDE embedding.
// Real LLM HyDE (when ChatProvider available) should prefer GenerateHydePassage.
func HydePassage(query string) string {
	q := RewriteQuery(query)
	if q == "" {
		q = strings.TrimSpace(query)
	}
	if q == "" {
		return ""
	}
	return fmt.Sprintf(
		"本文说明 %s 的核心概念、工作原理与常见用法。相关实现细节、边界条件与工程实践亦一并覆盖。",
		q,
	)
}

// FuseHitsByRRF merges ranked hit lists with Reciprocal Rank Fusion (k=60).
func FuseHitsByRRF(lists [][]RetrievalHit, topK int) []RetrievalHit {
	if topK <= 0 {
		topK = 5
	}
	const rrfK = 60.0
	type agg struct {
		hit   RetrievalHit
		score float64
	}
	m := map[string]*agg{}
	for _, list := range lists {
		for rank, h := range list {
			if h.ChunkID == "" {
				continue
			}
			rrf := 1.0 / (rrfK + float64(rank+1))
			if a, ok := m[h.ChunkID]; ok {
				a.score += rrf
				if h.Score > a.hit.Score {
					a.hit = h
				}
			} else {
				cp := h
				m[h.ChunkID] = &agg{hit: cp, score: rrf}
			}
		}
	}
	out := make([]RetrievalHit, 0, len(m))
	for _, a := range m {
		h := a.hit
		h.Score = float32(a.score)
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ChunkID < out[j].ChunkID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}
