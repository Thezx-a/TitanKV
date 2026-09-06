package rag

// ---- M2: Router 查询路由 (规则版 v1) ----
//
// 目标态分层架构的决策点 (docs/RAG-EXPANSION-PLAN.md §1.2):
//
//	Query → Router ──► L1 Wiki 直答 (预编译页, 零检索成本)
//	              ├─► L2 单发混合检索 (BM25 ∥ HNSW → RRF, 便宜路径, 目标承载 80%+)
//	              └─► L3 Agentic 深挖 (M3: 带预算的多轮循环)
//
// v1 是纯规则路由 (零 LLM 调用, 决策 < 1ms), 每个决策带 reason 供
// 查询日志审计; 规则误判时客户端可用 debug=route 核查, 运维可先
// RAG_ENABLE_ROUTER=false 观察日志再放量.

import (
	"strings"
	"unicode/utf8"
)

// RoutePath 是路由决策的三个层级.
type RoutePath string

const (
	RouteL1Wiki     RoutePath = "L1_wiki"     // 预编译 wiki 页直答
	RouteL2Single   RoutePath = "L2_single"   // 单发混合检索 (BM25+dense+RRF)
	RouteL3Agentic  RoutePath = "L3_agentic"  // 多轮 agentic 深挖 (M3 接管)
	RouteL3Degraded RoutePath = "L3_degraded" // L3 信号但 agentic 未启用/超预算 → 降级 L2
)

// RouteDecision is one routing decision with a human-auditable reason.
type RouteDecision struct {
	Path   RoutePath `json:"path"`
	Reason string    `json:"reason"`
}

// QueryRouter routes queries across L1/L2/L3 by deterministic rules.
type QueryRouter struct {
	wikiQ *WikiQuerier // may be nil (wiki disabled → L1 不可达)
}

// NewQueryRouter constructs the router. wikiQ may be nil.
func NewQueryRouter(wikiQ *WikiQuerier) *QueryRouter {
	return &QueryRouter{wikiQ: wikiQ}
}

// 路由信号词. 提取为包级变量便于测试与调参.
var (
	// L3 开放式信号: 长查询 + 解释/分析类动词 → 单发检索大概率不足
	routeOpenEndedMarkers = []string{"为什么", "如何", "怎么", "分析", "总结", "梳理", "机制", "关系", "原理"}
	// L3 对比信号: 多实体比较
	routeComparisonMarkers = []string{"对比", "区别", "比较", "还是", " vs ", "vs.", "VS"}
)

const (
	routeOpenEndedMinRunes = 16 // 开放式判定的最短查询长度 (rune)
	routeL3MinRunes        = 8  // 对比判定的最短查询长度
	routeL1MaxRunes        = 40 // wiki 直答判定的最长查询 (超长视为复杂问题)
)

// Decide returns the routing decision for one query.
//
// 规则优先级 (先命中先路由):
//  1. 空/纯符号 → L2
//  2. 对比信号 (≥routeL3MinRunes) → L3 (即使提到 wiki 实体, 对比仍需多源汇总)
//  3. 开放式信号 (≥routeOpenEndedMinRunes) → L3
//  4. wiki 精确 slug 命中 → L1 (预编译页存在, 直接答)
//  5. 查询包含某 wiki 页标题 (短查询) → L1
//  6. 其余 → L2 单发
func (rt *QueryRouter) Decide(col, query string) RouteDecision {
	q := strings.TrimSpace(query)
	if q == "" {
		return RouteDecision{Path: RouteL2Single, Reason: "empty_query"}
	}
	runes := utf8.RuneCountInString(q)

	// L3: 对比型 (多实体比较, 单发 topK 难覆盖全部侧面)
	if runes >= routeL3MinRunes && containsAny(q, routeComparisonMarkers) {
		return RouteDecision{Path: RouteL3Agentic, Reason: "comparison"}
	}

	// L3: 开放式 (长解释/分析问题, 需要多轮规划)
	if runes >= routeOpenEndedMinRunes && containsAny(q, routeOpenEndedMarkers) {
		return RouteDecision{Path: RouteL3Agentic, Reason: "open_ended"}
	}

	// L1: wiki 精确命中
	if rt.wikiQ != nil && runes <= routeL1MaxRunes {
		if p, err := rt.wikiQ.store.ResolveSlugExact(col, q); err == nil && p != nil {
			return RouteDecision{Path: RouteL1Wiki, Reason: "wiki_exact_slug:" + p.Frontmatter.Slug}
		}
		if hit := rt.wikiTitleContained(col, q); hit != "" {
			return RouteDecision{Path: RouteL1Wiki, Reason: "wiki_title_match:" + hit}
		}
	}

	return RouteDecision{Path: RouteL2Single, Reason: "default"}
}

// wikiTitleContained returns the first wiki title fully contained in the query
// (or query equal to title), "" if none.
func (rt *QueryRouter) wikiTitleContained(col, query string) string {
	if rt.wikiQ == nil {
		return ""
	}
	idx, err := rt.wikiQ.store.LoadIndex(col)
	if err != nil || idx == nil {
		return ""
	}
	qLower := strings.ToLower(query)
	for _, e := range idx.Entries {
		t := strings.ToLower(strings.TrimSpace(e.Title))
		if t != "" && (strings.Contains(qLower, t) || qLower == t) {
			return e.Slug
		}
	}
	return ""
}

func containsAny(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// degradeToL2 rewrites an L3 decision when the agentic lane is unavailable
// (M3 未实现 / chat provider 缺失 / 预算超限), keeping the audit trail.
func degradeToL2(d RouteDecision, why string) RouteDecision {
	return RouteDecision{
		Path:   RouteL3Degraded,
		Reason: d.Reason + "→degraded(" + why + ")",
	}
}
