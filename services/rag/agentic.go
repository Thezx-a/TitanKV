package rag

// ---- M3: Agentic 检索循环 (带硬预算) ----
//
// 分层架构的 L3 层: 单发检索覆盖不了的多跳/对比问题, 用多轮
// "规划子查询 → 检索 → 评分 → 融合" 循环补齐, 预算硬封顶:
//
//	round 1: 原查询直查 → 评分足够 → 直接返回 (大多数到此结束)
//	round 2..N: 规划子查询 → 逐个检索 → RRF 融合全部轮次 → 再评分
//	超过总轮数预算 → 立即停, 返回已有结果 + degraded 标记
//
// LLM 钩子 (PlanFn/GradeFn) 可注入; 默认启发式实现零 LLM 依赖:
// 规划 = 多实体标记切分, 评分 = 查询词与命中文本的 token 覆盖数.
// 总轮数预算 = 1 (原查询) + MaxRounds (子查询轮), 绝不超限.

import (
	"context"
	"strings"
)

// AgenticConfig bounds the loop. 所有字段都有安全默认值.
type AgenticConfig struct {
	MaxRounds   int // 子查询轮数上限 (不含 round 1), 硬预算
	MinRelevant int // 判定"足够"所需的相关命中数
	SubQueryN   int // 每轮子查询数上限
}

func (c AgenticConfig) withDefaults() AgenticConfig {
	if c.MaxRounds <= 0 {
		c.MaxRounds = 2
	}
	if c.MinRelevant <= 0 {
		c.MinRelevant = 2
	}
	if c.SubQueryN <= 0 {
		c.SubQueryN = 2
	}
	return c
}

// PlanFn 规划子查询: 输入原查询, 返回至多 n 个子查询 (不含原查询).
// 返回空 = 无子查询可规划 (单实体问题), 循环提前结束.
type PlanFn func(ctx context.Context, query string, n int) []string

// GradeFn 评分: 判断当前命中集是否足以回答原查询.
type GradeFn func(ctx context.Context, query string, hits []RetrievalHit) (sufficient bool, relevant int)

// AgenticRound 记录一轮循环的审计信息 (响应可见).
type AgenticRound struct {
	Round      int      `json:"round"` // 1 = 原查询
	Query      string   `json:"query"`
	SubQueries []string `json:"sub_queries,omitempty"`
	Hits       int      `json:"hits"`     // 该轮后累计融合命中数
	Relevant   int      `json:"relevant"` // 评分相关命中数
	Sufficient bool     `json:"sufficient"`
	Note       string   `json:"note,omitempty"` // 错误/提前终止说明
}

// AgenticResult is the L3 retrieval outcome.
type AgenticResult struct {
	Hits       []RetrievalHit `json:"hits"`
	Rounds     []AgenticRound `json:"rounds"`
	RoundsUsed int            `json:"rounds_used"`
	Degraded   bool           `json:"degraded"` // 预算耗尽/子查询失败仍未达标
	Reason     string         `json:"reason"`   // 审计: sufficient / budget_exhausted / no_more_plans / subquery_error
}

// HitRetriever 是 agentic 循环对底层检索的最小依赖 (便于注入 stub 测试).
type HitRetriever interface {
	Retrieve(ctx context.Context, col, query string, topK int) ([]RetrievalHit, error)
}

// AgenticRetriever is the budgeted L3 loop over a base hybrid retriever.
type AgenticRetriever struct {
	retriever HitRetriever
	cfg       AgenticConfig
	planFn    PlanFn  // nil → 启发式切分
	gradeFn   GradeFn // nil → 启发式覆盖评分
}

// NewAgenticRetriever wraps a retriever with the budgeted loop.
func NewAgenticRetriever(r HitRetriever, cfg AgenticConfig) *AgenticRetriever {
	return &AgenticRetriever{retriever: r, cfg: cfg.withDefaults()}
}

// SetLLMHooks 注入 LLM 规划/评分 (nil 字段回退启发式).
func (a *AgenticRetriever) SetLLMHooks(plan PlanFn, grade GradeFn) {
	a.planFn = plan
	a.gradeFn = grade
}

// Retrieve runs the budgeted multi-round loop. 永不超过预算; 单轮
// 子查询错误记入审计并立即降级返回 (不 panic, 不阻塞响应).
func (a *AgenticRetriever) Retrieve(ctx context.Context, col, query string, topK int) (*AgenticResult, error) {
	// round 1: 原查询
	hits, err := a.retriever.Retrieve(ctx, col, query, topK)
	if err != nil {
		return nil, err
	}
	res := &AgenticResult{Hits: hits}
	a.record(ctx, res, 1, query, nil, hits, "")

	// round 2..N: 子查询轮 (总数 ≤ 1+MaxRounds)
	for len(res.Rounds) <= a.cfg.MaxRounds {
		if res.Rounds[len(res.Rounds)-1].Sufficient {
			res.Reason = "sufficient"
			return res, nil
		}
		subs := a.plan(ctx, query, a.cfg.SubQueryN)
		if len(subs) == 0 {
			res.Reason = "no_more_plans"
			return res, nil
		}
		merged := res.Hits
		failed := ""
		for _, sq := range subs {
			sh, err := a.retriever.Retrieve(ctx, col, sq, topK)
			if err != nil {
				failed = "retrieve_error:" + err.Error()
				break
			}
			merged = a.fuse(merged, sh)
		}
		// 记录审计 (融合后的命中集), 更新结果
		if failed != "" {
			a.record(ctx, res, len(res.Rounds)+1, query, subs, merged, failed)
			res.Hits = merged
			res.Degraded = true
			res.Reason = "subquery_error"
			return res, nil
		}
		a.record(ctx, res, len(res.Rounds)+1, query, subs, merged, "")
		res.Hits = merged
	}

	// 预算耗尽
	last := res.Rounds[len(res.Rounds)-1]
	res.Degraded = !last.Sufficient
	if res.Degraded {
		res.Reason = "budget_exhausted"
	} else {
		res.Reason = "sufficient"
	}
	return res, nil
}

// record appends one audited round (grading via gradeFn or heuristic).
func (a *AgenticRetriever) record(ctx context.Context, res *AgenticResult, round int, query string, subs []string, hits []RetrievalHit, note string) {
	sufficient, relevant := a.grade(ctx, query, hits)
	res.Rounds = append(res.Rounds, AgenticRound{
		Round: round, Query: query, SubQueries: subs,
		Hits: len(hits), Relevant: relevant, Sufficient: sufficient, Note: note,
	})
	res.RoundsUsed = round
	outcome := "insufficient"
	if sufficient {
		outcome = "sufficient"
	}
	RagAgenticRoundsTotal.WithLabelValues(outcome).Inc()
}

// plan: LLM 钩子优先, 否则启发式多实体切分.
func (a *AgenticRetriever) plan(ctx context.Context, query string, n int) []string {
	if a.planFn != nil {
		return a.planFn(ctx, query, n)
	}
	return splitMultiEntity(query, n)
}

// grade: LLM 钩子优先, 否则启发式 token 覆盖评分.
func (a *AgenticRetriever) grade(ctx context.Context, query string, hits []RetrievalHit) (bool, int) {
	if a.gradeFn != nil {
		return a.gradeFn(ctx, query, hits)
	}
	relevant := countRelevant(query, hits)
	return relevant >= a.cfg.MinRelevant, relevant
}

// fuse merges hit lists via RRF (rank-based, 无量纲, 复用 M1 融合器).
func (a *AgenticRetriever) fuse(lists ...[]RetrievalHit) []RetrievalHit {
	return FuseHitsByRRF(lists, 0) // topK=0 → 融合器默认, 不丢结果
}

// ---- 启发式实现 (零 LLM) ----

// 多实体切分标记: "A和B", "A与B", "A vs B", "A、B" 等.
// 裸 "和/与" 在技术查询里误切率低 (字内成词罕见), 换取中文无空格书写习惯的覆盖.
var agenticSplitMarkers = []string{" 和 ", "和", " 与 ", "与", " vs ", " VS ", "、", "还是"}

// splitMultiEntity 将多实体查询切成实体子查询; 无标记返回 nil.
// 例: "布隆过滤器和 WAL 的区别" → ["布隆过滤器", "WAL"] (剥掉尾部"的区别"等).
func splitMultiEntity(query string, n int) []string {
	q := strings.TrimSpace(query)
	var parts []string
	for _, m := range agenticSplitMarkers {
		if strings.Contains(q, m) {
			parts = strings.Split(q, m)
			break
		}
	}
	if len(parts) < 2 {
		return nil
	}
	out := make([]string, 0, n)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// 剥掉实体尾部粘带的问句成分
		p = strings.TrimSuffix(p, "的区别")
		p = strings.TrimSuffix(p, "区别")
		p = strings.TrimSpace(p)
		if p != "" && len(out) < n {
			out = append(out, p)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// countRelevant 统计与查询有 token 交集的命中数 (复用 BM25 分词器, 中英一致).
func countRelevant(query string, hits []RetrievalHit) int {
	qTokens := make(map[string]struct{})
	for _, tok := range tokenizeBM25(query) {
		qTokens[tok] = struct{}{}
	}
	relevant := 0
	for _, h := range hits {
		for _, tok := range tokenizeBM25(h.Text) {
			if _, ok := qTokens[tok]; ok {
				relevant++
				break
			}
		}
	}
	return relevant
}
