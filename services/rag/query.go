package rag

import (
	"strings"
	"unicode"
)

// RewriteQuery lightly normalizes a user query before embedding.
// Used when RAG_ENABLE_QUERY_REWRITE=true. Safe no-op for empty input.
func RewriteQuery(query string) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return ""
	}
	// collapse whitespace
	var b strings.Builder
	b.Grow(len(q))
	prevSpace := false
	for _, r := range q {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	q = strings.TrimSpace(b.String())
	// strip common Chinese / punctuation question fluff at the end
	for {
		cut := false
		for _, suf := range []string{"吗？", "吗?", "呢？", "呢?", "？", "?", "吗", "呢"} {
			if strings.HasSuffix(q, suf) {
				q = strings.TrimSpace(strings.TrimSuffix(q, suf))
				cut = true
				break
			}
		}
		if !cut {
			break
		}
	}
	return q
}

// ExpandQueryTerms returns debug tokens (optional tooling).
func ExpandQueryTerms(query string) []string {
	q := RewriteQuery(query)
	if q == "" {
		return nil
	}
	return tokenize(q)
}
