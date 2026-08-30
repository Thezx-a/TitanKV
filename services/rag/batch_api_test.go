package rag

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIngestBatchJSONReturnsTasks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := LoadConfig()
	cfg.AsyncIngest = false
	cfg.EnableWiki = false
	cfg.MinikvAddr = "" // will use? NewService needs minikv — use mem via custom setup

	store := NewStoreFromKV(NewMemKV())
	emb := NewHashEmbedder(16)
	idx := NewVectorIndex(16, "brute")
	chunker := NewChunker(64, 8)
	ing := NewIngester(store, chunker, emb, idx, cfg)
	ret := NewRetrieverOpts(emb, idx, store, NewReranker(false), 3, false)
	svc := &Service{
		cfg: cfg, store: store, index: idx, embedder: emb,
		ingester: ing, retriever: ret,
	}

	r := gin.New()
	r.POST("/api/rag/collections/:col/documents/batch", svc.IngestDocumentsBatch)

	body := `{"documents":[{"title":"a","text":"hello world alpha"},{"title":"b","text":"hello world beta"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/rag/collections/demo/documents/batch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Tasks []IngestTask `json:"tasks"`
		Count int          `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 || len(resp.Tasks) != 2 {
		t.Fatalf("want 2 tasks, got count=%d tasks=%d body=%s", resp.Count, len(resp.Tasks), w.Body.String())
	}
}

func TestIngestBatchEmptyRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := LoadConfig()
	cfg.EnableWiki = false
	store := NewStoreFromKV(NewMemKV())
	svc := &Service{cfg: cfg, store: store, ingester: NewIngester(store, NewChunker(64, 8), NewHashEmbedder(8), NewSideIndex(8), cfg)}
	r := gin.New()
	r.POST("/api/rag/collections/:col/documents/batch", svc.IngestDocumentsBatch)
	req := httptest.NewRequest(http.MethodPost, "/api/rag/collections/demo/documents/batch", strings.NewReader(`{"documents":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}
