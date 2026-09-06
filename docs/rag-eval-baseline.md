# RAG Eval Baseline（质量基线记录）

> 由 `make rag-eval` 生成；每次检索链路改动后更新本文件。
> 测试环境：local-mock hash embedding（词袋 FNV），MemKV，heuristic tokenizer，chunker v2（512/64），TopK=5。
> ⚠️ hash embedding 只有词级重叠信号，接入真实 embedding API 后绝对值会变，但**相对回归门禁**始终有效。

## M1 后基线（2026-09-06，BM25+dense 混合检索，生产默认）

| 指标 | dense-only | 混合检索 (BM25+RRF) | CI 门禁（新基线×0.9） |
|---|---|---|---|
| Recall@5 | 0.893 | **1.000** | ≥ 0.90 |
| MRR | 0.836 | **0.985** | ≥ 0.89 |
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
