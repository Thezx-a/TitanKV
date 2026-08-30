package rag

import (
	"strings"
	"testing"
)

func TestEstimateTokensCJKVsASCII(t *testing.T) {
	cjk := "中文测试一二三四" // 8 CJK
	ascii := "abcdefghijklmnop" // 16 ASCII letters
	cjkTok := estimateTokens(cjk)
	asciiTok := estimateTokens(ascii)
	if cjkTok <= 0 || asciiTok <= 0 {
		t.Fatalf("tokens must be positive: cjk=%d ascii=%d", cjkTok, asciiTok)
	}
	// ASCII denser per rune than CJK under v2 estimator (4 bytes≈1 token vs ~1.5 chars≈1)
	if asciiTok >= cjkTok*3 {
		t.Fatalf("ascii should not be wildly larger than CJK for similar length: cjk=%d ascii=%d", cjkTok, asciiTok)
	}
	// Old runes/2 would give both 8; v2 should differentiate
	if cjkTok == asciiTok {
		t.Fatalf("expected different estimates for CJK vs ASCII, both=%d", cjkTok)
	}
}

func TestSlideWindowSoftBoundaryPrefersSentenceEnd(t *testing.T) {
	// Long text with clear sentence boundaries; soft split should land near 。
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("这是一句完整的中文句子。")
	}
	text := b.String()
	pieces := slideWindowWithBoundaries(text, 32, 8)
	if len(pieces) < 2 {
		t.Fatalf("want multiple pieces, got %d", len(pieces))
	}
	// First piece should end at a sentence boundary (。)
	if !strings.HasSuffix(strings.TrimSpace(pieces[0]), "。") {
		t.Fatalf("soft boundary expected sentence end, got suffix %q", pieces[0][len(pieces[0])-min(12, len(pieces[0])):])
	}
}

func TestChunkerMarkdownHeadingPath(t *testing.T) {
	md := "# 总览\n\nintro\n\n## 存储\n\nbody\n\n### WAL\n\ndetail\n"
	ch := ChunkerFor("doc.md", "markdown")
	chunks := ch.Split(md)
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	foundWAL := false
	for _, c := range chunks {
		if strings.Contains(c.Heading, "WAL") {
			foundWAL = true
			if !strings.Contains(c.Heading, "存储") && !strings.Contains(c.Heading, "→") {
				// heading path should include parent when nested
				t.Logf("heading=%q (path preferred)", c.Heading)
			}
		}
	}
	if !foundWAL {
		t.Fatalf("expected WAL heading in chunks: %+v", chunks)
	}
	// # top-level must not be ignored
	foundTop := false
	for _, c := range chunks {
		if strings.Contains(c.Heading, "总览") || strings.Contains(c.Text, "intro") {
			foundTop = true
		}
	}
	if !foundTop {
		t.Fatal("top-level # heading content missing")
	}
}

func TestChunkerFAQPairs(t *testing.T) {
	faq := "Q: 什么是 TitanKV？\nA: 一个 LSM 存储引擎。\n\nQ: 支持删除吗？\nA: 支持 tombstone。\n"
	ch := ChunkerFor("faq.txt", "faq")
	chunks := ch.Split(faq)
	if len(chunks) < 2 {
		t.Fatalf("want ≥2 FAQ chunks, got %d: %+v", len(chunks), chunks)
	}
	for _, c := range chunks {
		if !strings.Contains(c.Text, "Q:") || !strings.Contains(c.Text, "A:") {
			t.Fatalf("FAQ chunk should keep Q/A pair: %q", c.Text)
		}
	}
}

func TestChunkerCodeFunctionBoundary(t *testing.T) {
	code := "package main\n\nfunc Foo() {\n\tx := 1\n\t_ = x\n}\n\nfunc Bar() {\n\ty := 2\n\t_ = y\n}\n"
	ch := ChunkerFor("main.go", "code")
	chunks := ch.Split(code)
	if len(chunks) < 2 {
		t.Fatalf("want function-split chunks, got %d: %+v", len(chunks), chunks)
	}
}

func TestChunkerVersionConstant(t *testing.T) {
	if ChunkerVersion == "" {
		t.Fatal("ChunkerVersion must be non-empty")
	}
	if ChunkerVersion == "v1" {
		t.Fatal("industrial chunker should bump past v1")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
