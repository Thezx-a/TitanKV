package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// ---- 启发式规划/评分 ----

func TestSplitMultiEntity(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"布隆过滤器和 WAL 的区别", []string{"布隆过滤器", "WAL"}},
		{"LSM Tree 与 B+树", []string{"LSM Tree", "B+树"}},
		{"MemTable vs SSTable", []string{"MemTable", "SSTable"}},
		{"布隆过滤器、布谷鸟过滤器", []string{"布隆过滤器", "布谷鸟过滤器"}},
		{"WAL 是什么", nil}, // 单实体
		{"随便看看", nil},
		{" 和 ", nil}, // 切完为空
	}
	for _, c := range cases {
		got := splitMultiEntity(c.in, 4)
		if len(got) != len(c.want) {
			t.Fatalf("split(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("split(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestSplitMultiEntityRespectsLimit(t *testing.T) {
	got := splitMultiEntity("A 和 B 和 C 和 D", 2)
	if len(got) != 2 {
		t.Fatalf("limit not respected: %v", got)
	}
}

func TestCountRelevant(t *testing.T) {
	hits := []RetrievalHit{
		{ChunkID: "c1/d-bloom/0", Text: "布隆过滤器通过位数组判断元素存在"},
		{ChunkID: "c1/d-wal/0", Text: "WAL 预写日志保证宕机恢复"},
		{ChunkID: "c1/d-x/0", Text: "完全无关的内容 xyz"},
	}
	n := countRelevant("布隆过滤器 和 WAL 的区别", hits)
	if n != 2 {
		t.Fatalf("relevant = %d, want 2", n)
	}
}

// ---- 循环机制 (注入 stub) ----

// stubRetriever 按查询前缀返回固定命中, 便于驱动多轮场景.
type stubRetriever struct {
	byPrefix map[string][]string // query 前缀 → chunkID 列表
	calls    int
	failOn   string // 该前缀触发错误
}

func (s *stubRetriever) Retrieve(ctx context.Context, col, query string, topK int) ([]RetrievalHit, error) {
	s.calls++
	if s.failOn != "" && strings.HasPrefix(query, s.failOn) {
		return nil, errors.New("boom")
	}
	for pfx, ids := range s.byPrefix {
		if strings.HasPrefix(query, pfx) {
			out := make([]RetrievalHit, 0, len(ids))
			for _, id := range ids {
				// 文本携带匹配前缀 → 查询 token 与文本 token 有交集 (启发式评分可判定)
				out = append(out, RetrievalHit{ChunkID: id, DocID: id, Text: "text of " + pfx + " " + id})
			}
			return out, nil
		}
	}
	return nil, nil
}

func newStubAgentic(stub *stubRetriever, maxRounds int) *AgenticRetriever {
	return NewAgenticRetriever(stub, AgenticConfig{MaxRounds: maxRounds})
}

func TestAgenticSufficientRound1(t *testing.T) {
	stub := &stubRetriever{byPrefix: map[string][]string{
		"good": {"c1/d1/0", "c1/d2/0"},
	}}
	a := NewAgenticRetriever(stub, AgenticConfig{MaxRounds: 2})
	// 启发式评分: 命中文本 "text of c1/d1/0" 含查询词 "good" → token 交集 → relevant
	res, err := a.Retrieve(context.Background(), "c1", "good query", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.RoundsUsed != 1 || res.Degraded {
		t.Fatalf("round1 sufficient expected, got %+v", res)
	}
	if res.Reason != "sufficient" {
		t.Fatalf("reason = %s", res.Reason)
	}
}

func TestAgenticMultiRoundThenSufficient(t *testing.T) {
	stub := &stubRetriever{byPrefix: map[string][]string{
		"vague": {"c1/d9/9"},      // round1: 1 个命中 (不足 MinRelevant=2)
		"布隆过滤器": {"c1/d-bloom/0"}, // 子查询 1
		"WAL":   {"c1/d-wal/0"},   // 子查询 2
	}}
	a := NewAgenticRetriever(stub, AgenticConfig{MaxRounds: 2})
	// 注入规划: vague → [布隆过滤器子, WAL子] (stub 按前缀命中)
	a.planFn = func(ctx context.Context, q string, n int) []string {
		return []string{"布隆过滤器子", "WAL子"}
	}
	res, err := a.Retrieve(context.Background(), "c1", "vague query", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rounds) != 3 { // 1 (原查询) + MaxRounds(2) 子查询轮, 同一 planFn 每轮同解 → 预算耗尽
		t.Fatalf("want 3 rounds (1+2), got %+v", res.Rounds)
	}
	if !res.Degraded || res.Reason != "budget_exhausted" {
		t.Fatalf("budget mechanics broken: %+v", res)
	}
	// 关键: 融合已把两轮命中的实体文档收进结果集
	ids := map[string]bool{}
	for _, h := range res.Hits {
		ids[h.ChunkID] = true
	}
	if !ids["c1/d-bloom/0"] || !ids["c1/d-wal/0"] {
		t.Fatalf("fusion across rounds missing entity docs: %+v", ids)
	}
	if !strings.Contains(res.Rounds[1].SubQueries[0], "布隆过滤器") {
		t.Fatalf("sub-queries not recorded: %+v", res.Rounds[1])
	}
}

func TestAgenticBudgetExhausted(t *testing.T) {
	stub := &stubRetriever{byPrefix: map[string][]string{
		"vague": {"c1/d9/9"},
	}}
	a := NewAgenticRetriever(stub, AgenticConfig{MaxRounds: 2, MinRelevant: 5})
	a.planFn = func(ctx context.Context, q string, n int) []string {
		return []string{"s1", "s2"} // 永远有子查询, 但永远不达标
	}
	res, err := a.Retrieve(context.Background(), "c1", "vague", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rounds) != 3 { // 1 + MaxRounds(2)
		t.Fatalf("budget exceeded: rounds = %d", len(res.Rounds))
	}
	if !res.Degraded || res.Reason != "budget_exhausted" {
		t.Fatalf("degraded flag: %+v", res)
	}
}

func TestAgenticNoMorePlans(t *testing.T) {
	stub := &stubRetriever{byPrefix: map[string][]string{
		"vague": {"c1/d9/9"},
	}}
	a := NewAgenticRetriever(stub, AgenticConfig{MaxRounds: 2, MinRelevant: 5})
	// planFn nil + 启发式对 "vague" 无标记 → nil → no_more_plans
	res, err := a.Retrieve(context.Background(), "c1", "vague", 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != "no_more_plans" || len(res.Rounds) != 1 {
		t.Fatalf("want no_more_plans after round1, got %+v", res)
	}
}

func TestAgenticSubqueryErrorDegrades(t *testing.T) {
	stub := &stubRetriever{
		byPrefix: map[string][]string{"vague": {"c1/d9/9"}},
		failOn:   "s1",
	}
	a := NewAgenticRetriever(stub, AgenticConfig{MaxRounds: 2})
	a.planFn = func(ctx context.Context, q string, n int) []string { return []string{"s1", "s2"} }
	res, err := a.Retrieve(context.Background(), "c1", "vague", 5)
	if err != nil {
		t.Fatal(err) // 子查询错误不外抛, 降级返回
	}
	if !res.Degraded || res.Reason != "subquery_error" {
		t.Fatalf("want subquery_error degrade, got %+v", res)
	}
	if !strings.Contains(res.Rounds[len(res.Rounds)-1].Note, "boom") {
		t.Fatalf("error not audited: %+v", res.Rounds)
	}
}

// ---- 端到端启发式 (真实 Retriever + hash embedder) ----

func TestAgenticHeuristicMultiEntity(t *testing.T) {
	store, ing, ret, gs := newGoldenHarness(t, true)
	ingestGolden(t, ing, store, gs)

	a := NewAgenticRetriever(ret, AgenticConfig{MaxRounds: 2, MinRelevant: 2})
	// 多实体对比: bloom + wal 两文档都要命中
	res, err := a.Retrieve(context.Background(), gs.Collection, "布隆过滤器 和 WAL 预写日志 的区别", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("no hits")
	}
	sawBloom, sawWal := false, false
	for _, h := range res.Hits {
		if strings.HasPrefix(h.ChunkID, gs.Collection+"/doc-bloom/") {
			sawBloom = true
		}
		if strings.HasPrefix(h.ChunkID, gs.Collection+"/doc-wal/") {
			sawWal = true
		}
	}
	if !sawBloom || !sawWal {
		t.Fatalf("multi-entity fusion missing entity docs: bloom=%v wal=%v hits=%+v",
			sawBloom, sawWal, res.Hits)
	}
	if res.Rounds[0].Sufficient && res.RoundsUsed != 1 {
		t.Fatalf("sufficient round1 should stop: %+v", res)
	}
}

// ---- handler 集成: L3 → agentic ----

func TestRetrieveEndpointL3Agentic(t *testing.T) {
	svc := newRouterTestService(t, true)
	// 手动接上 agentic (routerTestService 未建)
	svc.agentic = NewAgenticRetriever(svc.retriever, AgenticConfig{MaxRounds: 2})

	r := gin.New()
	r.POST("/api/rag/collections/:col/retrieve", svc.Retrieve)

	req := httptest.NewRequest(http.MethodPost, "/api/rag/collections/demo/retrieve",
		strings.NewReader(`{"query":"LSM Tree 和 Bloom Filter 的区别","top_k":3}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Route   RouteDecision  `json:"route"`
		Agentic *AgenticResult `json:"agentic"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Route.Path != RouteL3Agentic {
		t.Fatalf("route = %+v, want L3_agentic", resp.Route)
	}
	if resp.Agentic == nil || len(resp.Agentic.Rounds) == 0 {
		t.Fatalf("agentic audit missing: %+v", resp)
	}
}

func TestRetrieveEndpointL3AgenticDisabled(t *testing.T) {
	// EnableAgentic=false: L3 降级单发
	svc := newRouterTestService(t, true)
	svc.cfg.EnableAgentic = false
	svc.agentic = nil

	r := gin.New()
	r.POST("/api/rag/collections/:col/retrieve", svc.Retrieve)

	req := httptest.NewRequest(http.MethodPost, "/api/rag/collections/demo/retrieve",
		strings.NewReader(`{"query":"LSM Tree 和 Bloom Filter 的区别","top_k":3}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Route   RouteDecision  `json:"route"`
		Agentic *AgenticResult `json:"agentic"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Route.Path != RouteL3Degraded {
		t.Fatalf("route = %+v, want L3_degraded", resp.Route)
	}
	if resp.Agentic != nil {
		t.Fatalf("degraded path must not include agentic audit: %+v", resp)
	}
}
