package rag

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	reHeading   = regexp.MustCompile(`(?m)^(#{1,3})\s+(.+)$`)
	reWikiLink  = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]*)?(?:\|[^\]]+)?\]\]`)
	reMultiDash = regexp.MustCompile(`-+`)
)

// SlugifyTitle turns a heading/title into a stable URL-ish slug.
// Keeps CJK runes; lowercases ASCII; collapses separators to single '-'.
func SlugifyTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "untitled"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range title {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if r < 128 {
				b.WriteRune(unicode.ToLower(r))
			} else {
				b.WriteRune(r)
			}
			prevDash = false
		case r == ' ' || r == '-' || r == '_' || r == '/' || r == '.':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		default:
			// drop punctuation
		}
	}
	out := strings.Trim(b.String(), "-")
	out = reMultiDash.ReplaceAllString(out, "-")
	if out == "" {
		return "untitled"
	}
	return out
}

// ExtractFromMarkdown splits markdown by ATX headings and collects [[wikilink]] edges.
// srcDocID is recorded in each page's Sources.
func ExtractFromMarkdown(md, srcDocID string) ([]WikiPage, []WikiEdge) {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(md, "\n")

	type section struct {
		title string
		body string
	}
	var sections []section
	var title string
	var body strings.Builder
	started := false

	commit := func() {
		b := strings.TrimSpace(body.String())
		t := strings.TrimSpace(title)
		if t == "" && b == "" {
			return
		}
		if t == "" {
			first := b
			if i := strings.IndexByte(b, '\n'); i > 0 {
				first = b[:i]
			}
			first = strings.TrimSpace(strings.TrimLeft(first, "#"))
			if len([]rune(first)) > 40 {
				first = string([]rune(first)[:40])
			}
			if first == "" {
				first = "Untitled"
			}
			t = first
		}
		sections = append(sections, section{title: t, body: b})
	}

	for _, line := range lines {
		if m := reHeading.FindStringSubmatch(line); m != nil {
			if started || body.Len() > 0 || title != "" {
				commit()
			}
			title = strings.TrimSpace(m[2])
			body.Reset()
			started = true
			continue
		}
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(line)
	}
	commit()

	pages := make([]WikiPage, 0, len(sections))
	baseCount := map[string]int{}
	for _, sec := range sections {
		base := SlugifyTitle(sec.title)
		n := baseCount[base]
		baseCount[base] = n + 1
		slug := base
		if n > 0 {
			slug = base + "-" + itoa(n+1)
		}
		pageType := WikiPageConcept
		if len(pages) == 0 {
			pageType = WikiPageEntity
		}
		pages = append(pages, WikiPage{
			Frontmatter: WikiFrontmatter{
				Title:      sec.title,
				Slug:       slug,
				Type:       pageType,
				Sources:    []string{srcDocID},
				Confidence: "medium",
				CompileVer: 1,
			},
			Body:    sec.body,
			Summary: ruleSummary(sec.body),
		})
	}

	edges := make([]WikiEdge, 0)
	edgeSeen := map[string]bool{}
	for _, p := range pages {
		for _, m := range reWikiLink.FindAllStringSubmatch(p.Body, -1) {
			target := SlugifyTitle(strings.TrimSpace(m[1]))
			if target == "" || target == p.Frontmatter.Slug {
				continue
			}
			key := p.Frontmatter.Slug + "→" + target
			if edgeSeen[key] {
				continue
			}
			edgeSeen[key] = true
			edges = append(edges, WikiEdge{
				From: p.Frontmatter.Slug,
				To:   target,
				Rel:  "links_to",
			})
		}
	}
	return pages, edges
}

func ruleSummary(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	parts := strings.Split(body, "\n\n")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		p = reWikiLink.ReplaceAllString(p, "$1")
		runes := []rune(p)
		if len(runes) > 240 {
			return string(runes[:240]) + "…"
		}
		return p
	}
	runes := []rune(body)
	if len(runes) > 240 {
		return string(runes[:240]) + "…"
	}
	return body
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
