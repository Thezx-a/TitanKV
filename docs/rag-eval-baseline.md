# RAG Eval Baseline（质量基线记录）

> 由 `make rag-eval` 生成；每次检索链路改动后更新本文件。
> 测试环境：local-mock hash embedding（词袋 FNV），MemKV，heuristic tokenizer，chunker v2（512/64），TopK=5。
> ⚠️ hash embedding 只有词级重叠信号，接入真实 embedding API 后绝对值会变，但**相对回归门禁**始终有效。

## M1 后基线（2026-09-06，BM25+dense 混合检索，生产默认）

| 指标 | dense-only | 混合检索 (BM25+RRF) | CI 门禁（新基线×0.9） |
|---|---|---|---|
| Recall@5 | 0.893 | **1.000** | ≥ 0.90 |
| MRR | 0.836 | **0.985** | ≥ 0.89 |
| KeyPointRecall@5 (M4) | — | **1.000** | ≥ 0.95 |
| extractive faithfulness (M4) | — | **1.000** | = 1.0 (管道健全性) |

M4 说明：
- KeyPointRecall：检索命中文本对 golden `expected_points` 要点的覆盖率，防「ID 对但内容不含要点」的假阳性
- extractive faithfulness：以 top-1 原文为参照答案，对引用的忠实度必须恒为 1（评分器/hydration 管道自检）
- 坏答案注入验收：`TestFaithGateBlocksHallucination` 验证幻觉断言（引用中不存在的实体）被门禁拦截

## M5 后补记（2026-09-06，Wiki 版本化 + 审计）

- Wiki 页面版本化（`wiki:pver:{col}:{slug}:{ver}`）：compile 覆盖前自动归档当前版，`RAG_WIKI_KEEP_VERSIONS`（默认 3）淘汰旧版
- `POST .../wiki/pages/:slug/rollback`：撤销最近一次覆盖（version=0）或回滚到指定归档版，索引同步重建、动作落 wiki log
- `GET .../wiki/audit`：全页引用支撑度审计（复用 M4 faithfulness 规则，零 LLM），低于 0.6 的页面点名支撑不足句子并排序置顶
- E2E（`scripts/e2e_rag_wiki.sh`）：compile → minikv 原生协议直写幻觉页 → rollback → 内容恢复 v1 → audit faithfulness=1.0，全部 PASS
| 查询数 | 28（关键词 25 + 改述 3） | — |

说明：
- 关键词型查询在词袋 hash embedding 下即可高命中（词级重叠 → 余弦相似）
- 改述型查询（"为什么宕机后数据不会丢"、"如何判断一个键肯定不在某个 SSTable 里"）dense 单通道几乎必然失败——这是 M1（BM25）与 M3（agentic 循环）的改进空间
- golden set：`services/rag/testdata/golden.json`（8 文档 / 28 查询，chunk ID 自校验）

## 历史记录

| 日期 | 阶段 | Recall@5 | MRR | 变化 |
|---|---|---|---|---|
| 2026-09-06 | M0 基线（dense-only，brute 精确检索，平局确定性裁决） | 0.893 | 0.836 | — |
| 2026-09-06 | M1 混合检索（BM25 中文 bigram + RRF 融合） | 1.000 | 0.985 | Recall +0.107 / MRR +0.149；改述型查询全部命中 |
