package rag

// ---- M4: 答案忠实度 (faithfulness) 与要点覆盖评估 ----
//
// v1 纯规则, 零 LLM 依赖 (CI 可跑):
//
//	FaithfulnessScore: 答案句子的断言 token 是否被引用 chunk 支撑
//	  → 幻觉防护 (H9): 检测答案里"引用中不存在"的关键词
//	KeyPointRecall: 检索命中的 chunk 是否覆盖期望答案要点
//	  → 检索质量第三指标 (与 Recall@K / MRR 互补: 防"检索到了 ID 对
//	    但内容不含要点"的假阳性)
//
// LLM-as-judge (RAG_EVAL_FAITH=llm) 预留 FaithJudge 钩子, 不在 CI 路径.

import (
	"context"
	"fmt"
	"strings"
)

// FaithJudge 是 LLM 评分钩子: 返回 0~1 分与不支持句子列表.
type FaithJudge func(ctx context.Context, answer string, contexts []string) (float64, []string)

// evalStopwords 不参与断言判定的功能词 (中英混合常见虚词).
var evalStopwords = map[string]struct{}{
	"的": {}, "了": {}, "和": {}, "与": {}, "是": {}, "在": {}, "有": {}, "对": {},
	"the": {}, "a": {}, "an": {}, "of": {}, "to": {}, "in": {}, "is": {}, "are": {},
	"and": {}, "or": {}, "for": {}, "on": {}, "by": {}, "it": {}, "this": {}, "that": {},
}

// faithfulTokens 提取句子中的断言 token: 英文词(≥2) + 数字串 + CJK bigram, 去停用词.
func faithfulTokens(sentence string) []string {
	var out []string
	for _, tok := range tokenizeBM25(sentence) {
		if _, ok := evalStopwords[tok]; ok {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// splitSentences 按中英标点与换行切句.
func splitSentences(text string) []string {
	f := func(r rune) bool {
		return r == '。' || r == '！' || r == '？' || r == '；' || r == '\n' ||
			r == '.' || r == '!' || r == '?' || r == ';'
	}
	var out []string
	for _, s := range strings.FieldsFunc(text, f) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// FaithfulnessScore 规则版忠实度:
//   - 每句提取断言 token, 统计被 contexts 拼接文本支撑的比例
//   - 句子支撑率 ≥ faithSentenceSupportRatio 视为"被支撑"
//   - 无断言 token 的句子 (纯语气/连接) 不参与分母
//   - 返回 (0~1 分, 未被支撑的句子列表)
func FaithfulnessScore(answer string, contexts []string) (float64, []string) {
	sentences := splitSentences(answer)
	if len(sentences) == 0 {
		return 1, nil // 无答案不扣分 (上层决定如何处理空答案)
	}
	hay := strings.ToLower(strings.Join(contexts, "\n"))
	supported := 0
	var unsupported []string
	for _, s := range sentences {
		toks := faithfulTokens(s)
		if len(toks) == 0 {
			supported++ // 无断言, 视为中性
			continue
		}
		hit := 0
		for _, t := range toks {
			if strings.Contains(hay, t) {
				hit++
			}
		}
		if float64(hit)/float64(len(toks)) >= faithSentenceSupportRatio {
			supported++
		} else {
			unsupported = append(unsupported, s)
		}
	}
	return float64(supported) / float64(len(sentences)), unsupported
}

const faithSentenceSupportRatio = 0.6

// KeyPointRecall 计算检索命中文本对期望要点的覆盖率 (0~1).
// 要点按 faithfulTokens 切分, 全部 token 出现在任一命中文本中即视为覆盖.
func KeyPointRecall(points []string, hits []RetrievalHit) float64 {
	if len(points) == 0 {
		return 1 // 无要点不扣分
	}
	hay := make([]string, len(hits))
	for i, h := range hits {
		hay[i] = strings.ToLower(h.Text)
	}
	covered := 0
	for _, p := range points {
		toks := faithfulTokens(p)
		if len(toks) == 0 {
			covered++
			continue
		}
		ok := true
		for _, t := range toks {
			found := false
			for _, h := range hay {
				if strings.Contains(h, t) {
					found = true
					break
				}
			}
			if !found {
				ok = false
				break
			}
		}
		if ok {
			covered++
		}
	}
	return float64(covered) / float64(len(points))
}

// EvalQuery / EvalResult 扩展要点字段 (M4).
type FaithEvalResult struct {
	Queries        int     `json:"queries"`
	KeyPointRecall float64 `json:"key_point_recall"`
	Faithfulness   float64 `json:"faithfulness"` // extractive 参照答案的忠实度 (恒 1, 管道健全性检查)
	UnsupportedN   int     `json:"unsupported_answers"`
}

// EvaluateFaith 对 golden set 跑要点覆盖 + 忠实度管道检查.
// extractive 参照: 取 top-1 命中原文当答案 (提取式), 其忠实度必须为 1;
// 若 < 1 说明评分器或 hydration 管道有 bug (CI 健全性门禁).
func EvaluateFaith(ctx context.Context, ret *Retriever, col string, queries []EvalQuery, topK int) (*FaithEvalResult, error) {
	res := &FaithEvalResult{Queries: len(queries)}
	if res.Queries == 0 {
		return res, nil
	}
	sumKP, sumFaith := 0.0, 0.0
	for _, q := range queries {
		hits, err := ret.Retrieve(ctx, col, q.Query, topK)
		if err != nil {
			return nil, fmt.Errorf("retrieve %q: %w", q.Query, err)
		}
		sumKP += KeyPointRecall(q.ExpectedPoints, hits)
		// 健全性: 提取式答案 (top-1 原文) 对引用必须完全忠实
		if len(hits) > 0 {
			ctxTexts := make([]string, len(hits))
			for i, h := range hits {
				ctxTexts[i] = h.Text
			}
			f, _ := FaithfulnessScore(hits[0].Text, ctxTexts)
			sumFaith += f
			if f < 1 {
				res.UnsupportedN++
			}
		}
	}
	res.KeyPointRecall = sumKP / float64(res.Queries)
	res.Faithfulness = sumFaith / float64(res.Queries)
	return res, nil
}
