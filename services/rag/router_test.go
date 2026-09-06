package rag

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newRouterTestService 构造带 wiki 页与 Router 的 Service (MemKV, 无 LLM).
func newRouterTestService(t *testing.T, enableRouter bool) *Service {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := LoadConfig()
	cfg.AsyncIngest = false
	cfg.EnableWiki = false
	cfg.EnableRouter = enableRouter

	store := NewStoreFromKV(NewMemKV())
	emb := NewHashEmbedder(16)
	idx := NewVectorIndex(16, "brute")
	ing := NewIngester(store, NewChunker(64, 8), emb, idx, cfg)
	ret := NewRetrieverOpts(emb, idx, store, NewReranker(false), 3, false)
	svc := &Service{
		cfg: cfg, store: store, index: idx, embedder: emb,
		ingester: ing, retriever: ret,
	}
	// wiki: 两页 (lsm-tree / bloom-filter), 供 L1 路由命中
	wiki := NewWikiStore(store)
	svc.wiki = wiki
	svc.wikiQ = NewWikiQuerier(wiki)
	if err := wiki.SavePagesAndEdges("demo", []WikiPage{
		{Frontmatter: WikiFrontmatter{Title: "LSM Tree", Slug: "lsm-tree", Type: WikiPageEntity, Sources: []string{"d1"}}, Body: "LSM-Tree 是写优化存储结构。"},
		{Frontmatter: WikiFrontmatter{Title: "Bloom Filter", Slug: "bloom-filter", Type: WikiPageEntity, Sources: []string{"d2"}}, Body: "布隆过滤器用于快速判存。"},
	}, nil); err != nil {
		t.Fatalf("save wiki pages: %v", err)
	}
	if enableRouter {
		svc.router = NewQueryRouter(svc.wikiQ)
	}
	return svc
}

func TestRouterDecideRules(t *testing.T) {
	svc := newRouterTestService(t, true)
	rt := svc.router
	cases := []struct {
		name  string
		query string
		want  RoutePath
	}{
		{"empty", "", RouteL2Single},
		{"exact slug", "lsm-tree", RouteL1Wiki},
		{"title contained", "介绍一下 LSM Tree 的写路径", RouteL1Wiki},
		{"title exact (en)", "Bloom Filter", RouteL1Wiki},
		{"comparison", "LSM Tree 和 B+树 的区别是什么", RouteL3Agentic},
		{"open-ended long", "请分析 LSM-Tree 的 compaction 机制以及它如何平衡写放大与读放大之间的关系", RouteL3Agentic},
		{"short factual", "WAL 是什么", RouteL2Single},
		{"paraphrase zh", "为什么宕机后数据不会丢", RouteL2Single},
	}
	for _, c := range cases {
		got := rt.Decide("demo", c.query)
		if got.Path != c.want {
			t.Errorf("%s: Decide(%q) = %s (%s), want %s", c.name, c.query, got.Path, got.Reason, c.want)
		}
	}
}

func TestRouterL1RequiresWikiMatch(t *testing.T) {
	// 不在 wiki 里的名词性查询不能误路由到 L1
	svc := newRouterTestService(t, true)
	d := svc.router.Decide("demo", "MVCC 版本链")
	if d.Path == RouteL1Wiki {
		t.Fatalf("non-wiki entity routed to L1: %+v", d)
	}
}

func TestRouterNilWikiQuerier(t *testing.T) {
	// wiki 关闭时 Router 仍可用, L1 不可达
	rt := NewQueryRouter(nil)
	if d := rt.Decide("demo", "lsm-tree"); d.Path == RouteL1Wiki {
		t.Fatalf("L1 routed without wiki querier: %+v", d)
	}
	if d := rt.Decide("demo", "随便查查"); d.Path != RouteL2Single {
		t.Fatalf("want L2, got %+v", d)
	}
}

func TestRetrieveEndpointRouteL2(t *testing.T) {
	svc := newRouterTestService(t, true) // router on, 但查询无 wiki/L3 信号
	r := gin.New()
	r.POST("/api/rag/collections/:col/retrieve", svc.Retrieve)

	req := httptest.NewRequest(http.MethodPost, "/api/rag/collections/demo/retrieve",
		strings.NewReader(`{"query":"WAL 是什么","top_k":3}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Route RouteDecision `json:"route"`
		Count int           `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Route.Path != RouteL2Single || resp.Route.Reason != "default" {
		t.Fatalf("route = %+v", resp.Route)
	}
}

func TestRetrieveEndpointRouteL1(t *testing.T) {
	svc := newRouterTestService(t, true)
	r := gin.New()
	r.POST("/api/rag/collections/:col/retrieve", svc.Retrieve)

	req := httptest.NewRequest(http.MethodPost, "/api/rag/collections/demo/retrieve",
		strings.NewReader(`{"query":"LSM Tree","top_k":3}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Route     RouteDecision `json:"route"`
		WikiCount int           `json:"wiki_count"`
		Wiki      []WikiHit     `json:"wiki"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Route.Path != RouteL1Wiki {
		t.Fatalf("route = %+v", resp.Route)
	}
	if resp.WikiCount == 0 || len(resp.Wiki) == 0 {
		t.Fatalf("expected wiki hits, got %+v", resp)
	}
	if resp.Wiki[0].Slug != "lsm-tree" {
		t.Fatalf("expected lsm-tree page, got %+v", resp.Wiki[0])
	}
}

func TestRetrieveEndpointRouteL3Degraded(t *testing.T) {
	svc := newRouterTestService(t, true)
	r := gin.New()
	r.POST("/api/rag/collections/:col/retrieve", svc.Retrieve)

	req := httptest.NewRequest(http.MethodPost, "/api/rag/collections/demo/retrieve",
		strings.NewReader(`{"query":"LSM Tree 和 B+树 的区别","top_k":3}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Route RouteDecision `json:"route"`
		Count int           `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Route.Path != RouteL3Degraded {
		t.Fatalf("route = %+v, want L3_degraded", resp.Route)
	}
	if !strings.Contains(resp.Route.Reason, "agentic_disabled") {
		t.Fatalf("reason should carry audit trail: %+v", resp.Route)
	}
	if resp.Count < 0 {
		t.Fatal("degraded path must still return hits")
	}
}

func TestRetrieveEndpointRouterOff(t *testing.T) {
	// Router 关闭: 行为与 M1 完全一致 (route 字段标记 router_off, 无 wiki 字段)
	svc := newRouterTestService(t, false)
	r := gin.New()
	r.POST("/api/rag/collections/:col/retrieve", svc.Retrieve)

	req := httptest.NewRequest(http.MethodPost, "/api/rag/collections/demo/retrieve",
		strings.NewReader(`{"query":"LSM Tree","top_k":3}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	route, ok := resp["route"].(map[string]any)
	if !ok {
		t.Fatalf("route field missing: %s", w.Body.String())
	}
	if route["path"] != string(RouteL2Single) || route["reason"] != "router_off" {
		t.Fatalf("route = %+v", route)
	}
	if _, hasWiki := resp["wiki"]; hasWiki {
		t.Fatal("router off must not add wiki field")
	}
}

func TestDegradeToL2Audit(t *testing.T) {
	d := degradeToL2(RouteDecision{Path: RouteL3Agentic, Reason: "comparison"}, "agentic_pending_m3")
	if d.Path != RouteL3Degraded || !strings.Contains(d.Reason, "comparison") {
		t.Fatalf("degrade lost audit trail: %+v", d)
	}
}
