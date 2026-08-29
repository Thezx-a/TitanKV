package rag

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRewriteQueryNormalizes(t *testing.T) {
	in := "  TitanKV 是什么吗？  "
	out := RewriteQuery(in)
	if out == in {
		t.Fatalf("expected rewrite to change query, got %q", out)
	}
	if strings.Contains(out, "  ") {
		t.Fatalf("expected collapsed whitespace, got %q", out)
	}
	if strings.HasSuffix(out, "吗") || strings.HasSuffix(out, "？") {
		t.Fatalf("expected question fluff trimmed, got %q", out)
	}
}

func TestRewriteQueryEmpty(t *testing.T) {
	if RewriteQuery("   ") != "" {
		t.Fatal("empty input should stay empty")
	}
}

func TestEmbedTextsBatch(t *testing.T) {
	e := NewHashEmbedder(32)
	texts := []string{"alpha", "beta", "gamma"}
	vecs, err := EmbedTexts(context.Background(), e, texts, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 {
		t.Fatalf("want 3 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 32 {
			t.Fatalf("vec %d dim=%d", i, len(v))
		}
	}
	single, err := e.Embed(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !floatSliceClose(single, vecs[0]) {
		t.Fatal("batch embed of alpha should match single Embed")
	}
}

func floatSliceClose(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		d := a[i] - b[i]
		if d < -1e-5 || d > 1e-5 {
			return false
		}
	}
	return true
}

func TestAssemblePromptIncludesHistory(t *testing.T) {
	hits := []RetrievalHit{{DocID: "d1", Text: "ctx", Heading: "H", Score: 0.9}}
	history := []ChatMessage{
		{Role: "user", Content: "上一问"},
		{Role: "assistant", Content: "上一答"},
	}
	p := assemblePromptWithHistory("当前问题", hits, history)
	if !strings.Contains(p, "上一问") || !strings.Contains(p, "上一答") {
		t.Fatalf("prompt missing history: %s", p)
	}
	if !strings.Contains(p, "当前问题") {
		t.Fatal("prompt missing current query")
	}
	if !strings.Contains(p, "ctx") {
		t.Fatal("prompt missing retrieval context")
	}
}

func TestParsePlainText(t *testing.T) {
	doc, err := ParseDocument("note.md", []byte("# hi\n\nhello"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Text, "hello") {
		t.Fatalf("text=%q", doc.Text)
	}
	if doc.DocType != "markdown" && doc.DocType != "plain" && doc.DocType != "md" {
		// accept markdown or plain
		if doc.DocType == "" {
			t.Fatal("DocType empty")
		}
	}
}

func TestParsePDFMissingCommandFailsClosed(t *testing.T) {
	_, err := ParseDocumentWithCommand("x.pdf", []byte("%PDF-1.4 fake"), "pdftotext-not-installed-xyz")
	if err == nil {
		t.Fatal("expected fail-closed when pdf command missing")
	}
}

func TestIngestQueueFull(t *testing.T) {
	p := NewIngestPool(IngestPoolConfig{Workers: 1, QueueSize: 1}, func(context.Context, ingestJob) {})
	defer p.Close()
	// fill the single slot without a worker draining: use 0 workers via blocked consumer
	p2 := &IngestPool{
		queue: make(chan ingestJob, 1),
	}
	job := ingestJob{Col: "c", Title: "t", Source: "s", Text: "hello"}
	if err := p2.Enqueue(job); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	if err := p2.Enqueue(job); err != ErrIngestQueueFull {
		t.Fatalf("want ErrIngestQueueFull, got %v", err)
	}
	_ = p
}

func TestTrimChatHistoryTurns(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "1"},
		{Role: "assistant", Content: "a"},
		{Role: "user", Content: "2"},
		{Role: "assistant", Content: "b"},
		{Role: "user", Content: "3"},
		{Role: "assistant", Content: "c"},
	}
	out := trimChatHistory(msgs, 2) // 2 turns = 4 messages
	if len(out) != 4 {
		t.Fatalf("want 4 msgs, got %d", len(out))
	}
	if out[0].Content != "2" || out[3].Content != "c" {
		t.Fatalf("unexpected trim: %+v", out)
	}
}

func TestRagMetricsRegistered(t *testing.T) {
	// smoke: counters should be non-nil after package init
	if RagIngestTotal == nil || RagRetrieveDuration == nil {
		t.Fatal("rag metrics not registered")
	}
	RagIngestTotal.WithLabelValues("success").Inc()
	time.Sleep(0)
}
