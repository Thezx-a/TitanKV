package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Reranker re-scores retrieval hits. Lexical overlap is the default MVP;
// NewHTTPReranker adds an optional sidecar (bge-reranker compatible) with lexical fallback.
type Reranker struct {
	enabled bool
	url     string
	client  *http.Client
}

func NewReranker(enabled bool) *Reranker {
	return &Reranker{enabled: enabled}
}

// NewHTTPReranker constructs a reranker that POSTs to url when enabled.
// Expected sidecar response: {"scores":[float,...]} aligned with input docs.
func NewHTTPReranker(url string, enabled bool) *Reranker {
	return &Reranker{
		enabled: enabled,
		url:     strings.TrimRight(url, "/"),
		client:  &http.Client{Timeout: 8 * time.Second},
	}
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

// Rerank blends vector score with lexical overlap, or sidecar scores when configured.
func (r *Reranker) Rerank(query string, hits []RetrievalHit) []RetrievalHit {
	if !r.enabled || len(hits) <= 1 {
		return hits
	}
	if r.url != "" {
		if out, err := r.rerankHTTP(query, hits); err == nil {
			return out
		}
		// fall through to lexical on sidecar failure
	}
	return r.rerankLexical(query, hits)
}

func (r *Reranker) rerankLexical(query string, hits []RetrievalHit) []RetrievalHit {
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
	sortHitsDesc(hits)
	return hits
}

func (r *Reranker) rerankHTTP(query string, hits []RetrievalHit) ([]RetrievalHit, error) {
	docs := make([]string, len(hits))
	for i, h := range hits {
		docs[i] = h.Text
		if h.Heading != "" {
			docs[i] = h.Heading + "\n" + h.Text
		}
	}
	body, _ := json.Marshal(map[string]any{
		"query": query,
		"docs":  docs,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rerank sidecar status %d", resp.StatusCode)
	}
	var out struct {
		Scores []float32 `json:"scores"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Scores) != len(hits) {
		return nil, fmt.Errorf("rerank sidecar score len %d != hits %d", len(out.Scores), len(hits))
	}
	for i := range hits {
		hits[i].Score = out.Scores[i]
	}
	sortHitsDesc(hits)
	return hits, nil
}

func sortHitsDesc(hits []RetrievalHit) {
	for i := 1; i < len(hits); i++ {
		j := i
		for j > 0 && hits[j].Score > hits[j-1].Score {
			hits[j], hits[j-1] = hits[j-1], hits[j]
			j--
		}
	}
}
