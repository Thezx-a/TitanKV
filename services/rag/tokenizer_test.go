package rag

import (
	"testing"
)

func TestHeuristicTokenEstimatorPositive(t *testing.T) {
	e := NewHeuristicTokenEstimator()
	n := e.Count("Hello 世界")
	if n <= 0 {
		t.Fatalf("count=%d", n)
	}
}

func TestTiktokenEstimatorMatchesKnownShort(t *testing.T) {
	e, err := NewTiktokenEstimator("cl100k_base")
	if err != nil {
		t.Skipf("tiktoken unavailable: %v", err)
	}
	// cl100k_base: "hello world" is typically 2 tokens
	n := e.Count("hello world")
	if n < 2 || n > 4 {
		t.Fatalf("unexpected tiktoken count for 'hello world': %d", n)
	}
	// CJK should not use runes/2 heuristic
	cjk := e.Count("中文测试一二三四")
	if cjk <= 0 {
		t.Fatal("cjk zero")
	}
}

func TestChunkerUsesInjectedEstimator(t *testing.T) {
	// Count ≈ rune length so soft window can actually cut.
	fixed := TokenEstimatorFunc(func(s string) int {
		return len([]rune(s))
	})
	ch := NewChunker(20, 4)
	ch.Estimator = fixed
	ch.Mode = "plain"
	text := "这是一句完整的中文句子。这是第二句完整的中文句子。这是第三句完整句子。这是第四句。"
	chunks := ch.Split(text)
	if len(chunks) < 2 {
		t.Fatalf("injected estimator should force multi-chunk, got %d text_tokens=%d", len(chunks), fixed.Count(text))
	}
}

func TestActiveChunkerVersionReflectsTokenizer(t *testing.T) {
	v := ActiveChunkerVersion("heuristic")
	if v == "" || v == "v1" {
		t.Fatalf("bad version %q", v)
	}
	v2 := ActiveChunkerVersion("tiktoken")
	if v2 == v {
		t.Fatalf("tiktoken version should differ from heuristic: both %q", v)
	}
}
