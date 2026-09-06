package rag

// ---- M5: Wiki 引用支撑度审计 (H9 幻觉防护) ----
//
// LLM Wiki 的核心风险: 摘要幻觉会被固化为事实并沿双链持久传播,
// 与 RAG 的一次性错误不同, 必须定期审计 (docs/RAG-EXPANSION-PLAN.md M5).
//
// 规则 (复用 M4 faithfulness): 页面正文每个句子的断言 token 是否被
// 其 Frontmatter.Sources 指向的原始 chunk 支撑; 支撑率低的页面列入
// 审计报告, 供人工核查或回滚.

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// wikiAuditEntryThreshold 单页忠实度低于该值视为"支撑不足".
const wikiAuditEntryThreshold = 0.6

// WikiAuditEntry is one page's source-support audit result.
type WikiAuditEntry struct {
	Slug         string   `json:"slug"`
	Title        string   `json:"title"`
	Sources      []string `json:"sources"`
	SourceChunks int      `json:"source_chunks"`
	Faithfulness float64  `json:"faithfulness"`
	OK           bool     `json:"ok"`
	Unsupported  []string `json:"unsupported,omitempty"` // 支撑不足的句子
}

// WikiAuditReport is a full-collection audit pass.
type WikiAuditReport struct {
	Col             string           `json:"col"`
	Pages           int              `json:"pages"`
	Audited         int              `json:"audited"` // 有 source 可对照的页数
	Skipped         int              `json:"skipped"` // source chunk 缺失 (doc 已删)
	AvgFaithfulness float64          `json:"avg_faithfulness"`
	BelowThreshold  int              `json:"below_threshold"`
	Entries         []WikiAuditEntry `json:"entries"`
	AuditedAt       int64            `json:"audited_at"`
}

// AuditWikiSources audits every wiki page in col against its source chunks.
// zero-LLM: 复用 M4 规则评分; ctx 供未来 LLM-as-judge 钩子使用.
func AuditWikiSources(ctx context.Context, w *WikiStore, store *Store, col string) (*WikiAuditReport, error) {
	idx, err := w.LoadIndex(col)
	if err != nil {
		return nil, err
	}
	report := &WikiAuditReport{Col: col, AuditedAt: time.Now().Unix()}
	if idx == nil {
		return report, nil
	}
	report.Pages = len(idx.Entries)

	sum := 0.0
	for _, e := range idx.Entries {
		page, err := w.GetPage(col, e.Slug)
		if err != nil || page == nil {
			continue
		}
		entry := WikiAuditEntry{
			Slug: e.Slug, Title: e.Title, Sources: page.Frontmatter.Sources,
		}
		// 收集全部 source chunk 正文作为对照上下文
		var ctxTexts []string
		for _, src := range page.Frontmatter.Sources {
			chunks, err := store.ListChunks(col, src)
			if err != nil {
				continue
			}
			for _, c := range chunks {
				ctxTexts = append(ctxTexts, c.Text)
			}
		}
		entry.SourceChunks = len(ctxTexts)
		if len(ctxTexts) == 0 {
			report.Skipped++
			entry.OK = false
			entry.Unsupported = []string{"(no source chunks available)"}
			report.Entries = append(report.Entries, entry)
			continue
		}
		f, unsupported := FaithfulnessScore(page.Body, ctxTexts)
		entry.Faithfulness = f
		entry.OK = f >= wikiAuditEntryThreshold
		if !entry.OK {
			entry.Unsupported = unsupported
			report.BelowThreshold++
		}
		report.Audited++
		sum += f
		report.Entries = append(report.Entries, entry)
	}
	if report.Audited > 0 {
		report.AvgFaithfulness = sum / float64(report.Audited)
	}
	// 审计明细排序: 有 source 的低支撑页排前 (人工核查优先), 无 source 页排尾
	sort.Slice(report.Entries, func(i, j int) bool {
		a, b := report.Entries[i], report.Entries[j]
		aOK, bOK := a.SourceChunks > 0, b.SourceChunks > 0
		if aOK != bOK {
			return aOK // 有 source 的在前
		}
		if aOK {
			return a.Faithfulness < b.Faithfulness
		}
		return false
	})
	// 审计动作落 wiki log (与 compile/delete 同一审计流)
	_ = w.AppendLog(col, WikiLogEntry{
		Action: "audit", Subject: fmt.Sprintf("%d pages", report.Pages),
		Detail: fmt.Sprintf("avg=%.2f below_threshold=%d", report.AvgFaithfulness, report.BelowThreshold),
	})
	return report, nil
}
