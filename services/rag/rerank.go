package rag

import (
	"strings"
	"unicode"
)

// Reranker re-scores retrieval hits using lightweight lexical overlap (MVP stand-in for bge-reranker).
type Reranker struct {
	enabled bool
}

func NewReranker(enabled bool) *Reranker {
	return &Reranker{enabled: enabled}
}

func rerankTokenize(s string) map[string]struct{} {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	out := make(map[string]struct{})
	for _, tok := range strings.Fields(b.String()) {
		if len(tok) >= 2 {
			out[tok] = struct{}{}
		}
	}
	return out
}

// Rerank blends vector score (70%) with token overlap (30%).
func (r *Reranker) Rerank(query string, hits []RetrievalHit) []RetrievalHit {
	if !r.enabled || len(hits) <= 1 {
		return hits
	}
	qtok := rerankTokenize(query)
	for i := range hits {
		ctok := rerankTokenize(hits[i].Text + " " + hits[i].Heading)
		var overlap float32
		if len(qtok) > 0 {
			var matched int
			for t := range qtok {
				if _, ok := ctok[t]; ok {
					matched++
				}
			}
			overlap = float32(matched) / float32(len(qtok))
		}
		hits[i].Score = hits[i].Score*0.7 + overlap*0.3
	}
	// insertion sort desc by score (small k)
	for i := 1; i < len(hits); i++ {
		j := i
		for j > 0 && hits[j].Score > hits[j-1].Score {
			hits[j], hits[j-1] = hits[j-1], hits[j]
			j--
		}
	}
	return hits
}
