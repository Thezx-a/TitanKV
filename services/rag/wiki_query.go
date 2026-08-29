package rag

import (
	"context"
	"fmt"
	"strings"
)

// WikiHit is a compiled-page hit (before optional vector fallback).
type WikiHit struct {
	Slug        string  `json:"slug"`
	Title       string  `json:"title"`
	Score       float32 `json:"score"`
	BodyExcerpt string  `json:"excerpt"`
	Source      string  `json:"source"` // wiki | vector_fallback
}

// WikiGraphView is the adjacency response for GET .../wiki/graph.
type WikiGraphView struct {
	Slug      string                `json:"slug"`
	Depth     int                   `json:"depth"`
	Out       []WikiEdge            `json:"out"`
	In        []WikiEdge            `json:"in"`
	Neighbors map[string][]WikiEdge `json:"neighbors,omitempty"`
}

// BuildWikiGraph returns out/in edges; depth=2 also expands outbound neighbors.
func BuildWikiGraph(w *WikiStore, col, slug string, depth int) (*WikiGraphView, error) {
	if depth < 1 {
		depth = 1
	}
	if depth > 2 {
		depth = 2
	}
	out, err := w.ListEdges(col, slug, true)
	if err != nil {
		return nil, err
	}
	in, err := w.ListEdges(col, slug, false)
	if err != nil {
		return nil, err
	}
	g := &WikiGraphView{Slug: slug, Depth: depth, Out: out, In: in}
	if depth >= 2 {
		nodes := map[string][]WikiEdge{}
		for _, e := range out {
			if more, err := w.ListEdges(col, e.To, true); err == nil {
				nodes[e.To] = more
			}
		}
		g.Neighbors = nodes
	}
	return g, nil
}

// WikiQuerier resolves queries against wiki pages / index.
type WikiQuerier struct {
	store *WikiStore
}

// NewWikiQuerier constructs a querier.
func NewWikiQuerier(store *WikiStore) *WikiQuerier {
	return &WikiQuerier{store: store}
}

// Resolve looks up exact slug/title, then fuzzy title contains in index.
func (q *WikiQuerier) Resolve(col, query string, limit int) ([]WikiHit, error) {
	if limit <= 0 {
		limit = 5
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	hits := make([]WikiHit, 0, limit)
	seen := map[string]bool{}

	add := func(p *WikiPage, score float32) {
		if p == nil || seen[p.Frontmatter.Slug] {
			return
		}
		seen[p.Frontmatter.Slug] = true
		excerpt := p.Summary
		if excerpt == "" {
			excerpt = p.Body
		}
		runes := []rune(excerpt)
		if len(runes) > 400 {
			excerpt = string(runes[:400]) + "…"
		}
		hits = append(hits, WikiHit{
			Slug: p.Frontmatter.Slug, Title: p.Frontmatter.Title,
			Score: score, BodyExcerpt: excerpt, Source: "wiki",
		})
	}

	if p, err := q.store.ResolveSlugExact(col, query); err != nil {
		return nil, err
	} else if p != nil {
		add(p, 1.0)
		// expand neighbors depth=1
		if edges, e := q.store.ListEdges(col, p.Frontmatter.Slug, true); e == nil {
			for _, edge := range edges {
				if len(hits) >= limit {
					break
				}
				if np, _ := q.store.GetPage(col, edge.To); np != nil {
					add(np, 0.8)
				}
			}
		}
	}

	if len(hits) >= limit {
		return hits[:limit], nil
	}

	idx, err := q.store.LoadIndex(col)
	if err != nil {
		return hits, err
	}
	qLower := strings.ToLower(query)
	qSlug := SlugifyTitle(query)
	for _, e := range idx.Entries {
		if len(hits) >= limit {
			break
		}
		titleLower := strings.ToLower(e.Title)
		if e.Slug == qSlug || strings.Contains(titleLower, qLower) || strings.Contains(qLower, strings.ToLower(e.Title)) {
			if p, _ := q.store.GetPage(col, e.Slug); p != nil {
				add(p, 0.7)
			}
		}
	}
	return hits, nil
}

// WikiFirstRetrieve tries wiki pages first; if fewer than topK, falls back to vector retrieve.
func WikiFirstRetrieve(ctx context.Context, col, query string, topK int, wq *WikiQuerier, r *Retriever) ([]WikiHit, []RetrievalHit, error) {
	if topK <= 0 {
		topK = 5
	}
	wikiHits, err := wq.Resolve(col, query, topK)
	if err != nil {
		return nil, nil, err
	}
	if len(wikiHits) >= topK {
		return wikiHits[:topK], nil, nil
	}
	need := topK - len(wikiHits)
	fallback, err := r.Retrieve(ctx, col, query, need*2)
	if err != nil {
		return wikiHits, nil, err
	}
	if len(fallback) > need {
		fallback = fallback[:need]
	}
	return wikiHits, fallback, nil
}

func assembleWikiPrompt(query string, wiki []WikiHit, fallback []RetrievalHit, history []ChatMessage) string {
	var b strings.Builder
	b.WriteString("你是 TitanWiki 助手。优先依据已编译 wiki 页回答；不足时参考原文片段。\n")
	if len(history) > 0 {
		b.WriteString("<history>\n")
		for _, m := range history {
			b.WriteString(m.Role)
			b.WriteString(": ")
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
		b.WriteString("</history>\n")
	}
	b.WriteString("<context>\n")
	for _, h := range wiki {
		b.WriteString(fmt.Sprintf("[wiki:%s] %s\n%s\n\n", h.Slug, h.Title, h.BodyExcerpt))
	}
	for _, h := range fallback {
		b.WriteString(fmt.Sprintf("[doc:%s] %s\n%s\n\n", h.DocID, h.Heading, h.Text))
	}
	b.WriteString("</context>\n\n")
	b.WriteString("问题: ")
	b.WriteString(query)
	return b.String()
}
