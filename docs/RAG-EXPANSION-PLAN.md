# TitanKV RAG 层扩展计划（v3.0）

> 分支：feat/industrialization
> 日期：2026-09-06
> 前置文档：[RAG-ARCHITECTURE.md](RAG-ARCHITECTURE.md)、[RagKv.md](../RagKv.md)
> 实施方式：**纵向切片（tracer bullet）**——每个模块打通"存储→索引→检索→API→E2E 验证"全链路，测试通过后才进入下一模块

---

## 1. 背景与目标

### 1.1 现状勘误

services/rag（~7.4k 行 Go）**已不是 naive RAG**，现有能力：

| 能力 | 代码位置 | 状态 |
|---|---|---|
| HNSW 向量索引（自研） | hnsw_index.go | ✅ |
| multi-query + RRF 融合(k=60) | query_expand.go | ✅ |
| HTTP 重排 + 词法降级 | rerank.go | ✅ |
| HyDE / 查询改写 | retrieve.go | ✅ |
| 结构感知 chunking（markdown 标题/FAQ） | chunker.go | ✅ |
| L1 Wiki 层（compile/store/querier/双链图/wiki优先检索） | wiki_*.go | ✅ |
| 检索评估（Recall@K + MRR + API） | eval.go | ✅ |
| 异步 ingest / 快照 / metrics | worker.go, metrics.go | ✅ |

### 1.2 目标态架构

```
Query → Router ──► L1 Wiki 直答 (WikiFirstRetrieve)
              ├─► L2 BM25 ∥ HNSW → RRF → rerank   （单发便宜路径，目标承载 80%+）
              └─► L3 Agentic：grade → rewrite → retry（硬预算：max 2 轮 + token 上限）
评估闭环：golden set → Recall@K / MRR / faithfulness → make test / make eval
存储底座：全部落 minikv（chunk / wiki 页 / 倒排 posting / 查询日志）
```

行业坐标（文档引用用）：RAGFlow（全栈引擎）、rageoffer/ragent（全链路架构参考）、RouteRAG（ACL 2026，路由方向）、Karpathy llm-wiki（L1 层思想源头）。

---

## 2. 需求清单

### 2.1 显性需求（明确要做的）

| # | 需求 | 对应模块 |
|---|---|---|
| R1 | BM25 稀疏检索通道，与 dense 并行，RRF 融合 | M1 |
| R2 | Router：按查询特征路由 Wiki直答 / 单发chunk / Agentic | M2 |
| R3 | Agentic 循环：grader 评分 + 改写重试 + 硬预算封顶 | M3 |
| R4 | faithfulness 答案忠实度评估 + golden dataset 落盘进 CI | M0/M4 |
| R5 | RagKv.md / RAG-ARCHITECTURE.md 更新为分层架构讲稿 | M6 |

### 2.2 隐性需求（不写出来但必须满足的约束）

| # | 需求 | 说明 | 贯穿模块 |
|---|---|---|---|
| H1 | **纯 Go、零新外部依赖** | 不引入 Qdrant/Milvus/Python sidecar；自研 HNSW 保留 | 全部 |
| H2 | **minikv 是唯一持久化底座** | 倒排 posting、golden set、审计日志全落 minikv / 或仓库内 JSON，不用外部 DB | M1/M4 |
| H3 | **向后兼容** | 现有 API 路径与请求/响应结构不变；新功能默认关闭或走新端点 | 全部 |
| H4 | **配置开关化** | 沿用 `RAG_ENABLE_*` 环境变量模式，所有新路径可独立关闭 | M1-M3 |
| H5 | **不破坏存量测试** | 现有 go test（68 用例）与 E2E 冒烟必须持续绿；Tiktoken 用例延续 `-skip Tiktoken` | 全部 |
| H6 | **热路径性能不回退** | 新功能关闭时，检索延迟与改动前持平（benchmark 对比）；开启时 Router 决策 < 10ms（规则版） | M1/M2 |
| H7 | **成本可控** | L3 路径必须有硬预算（轮数 + token 数），超限降级返回并打日志 | M3 |
| H8 | **可观测** | 每次查询记录路由决策、命中层级、轮数、token 消耗；接入现有 metrics.go 模式 | M2/M3 |
| H9 | **幻觉防护** | Wiki/Agentic 产物带引用溯源；golden set eval 防质量回退 | M3/M4 |
| H10 | **面试可讲性** | 每个模块的"为什么不用现成轮子"答案写进文档 | M6 |
| H11 | **中英混合语料** | BM25 分词需处理中文（现有 tokenizer.go 复用/扩展） | M1 |

---

## 3. 纵向切片原则

每个模块 = 一条**端到端竖切**，完成的定义（DoD）统一为：

1. 代码落地（含单元测试）
2. `go test ./services/rag/ -skip Tiktoken` 全绿
3. **E2E 冒烟通过**：`make run-all` 后按模块验收脚本跑通（ingest → 新路径触发 → 断言响应）
4. eval 对比数据落盘（改动前后 Recall@K / MRR 至少不回退，M1 要求提升可量化）
5. 该模块涉及文档同步更新

**严禁**：多模块并行开发、只写代码不跑 E2E、跳过 M0 直接做上层。

---

## 4. 模块计划

### M0：评估基线加固（~0.5~1 天）——地基，最先做

**目标**：先有尺子，再动手术。后续每个模块的质量结论都由它出。

任务：
- [ ] golden dataset 落盘：`services/rag/testdata/golden.json`（≥50 条：query + relevant chunk IDs，覆盖中文/英文/单跳/多跳/FAQ 型）
- [ ] eval.go 扩展：支持从 JSON 文件加载；输出对比表（改动前后）
- [ ] Makefile：`make rag-eval` target（含 `-skip Tiktoken` 规避）
- [ ] 跑一次基线，记录当前 Recall@K / MRR 到 `docs/rag-eval-baseline.md`

验收（E2E）：
```bash
make rag-eval   # 输出 baseline：Recall@K 与 MRR 有数字，golden.json 断言非空
```

### M1：BM25 稀疏检索通道（~1.5~2 天）——性价比最高的质量提升

**目标**：检索从单通道变双通道，BM25 倒排存 minikv。

任务：
- [ ] `bm25.go`：倒排索引构建（term → posting list，key 布局 `bm25/{col}/{term}/{chunkID}`，Iterator 前缀扫回查）
- [ ] 分词：复用 tokenizer.go；中文按 bigram 或现有切词，英文 lowercase+stem（轻量即可）
- [ ] BM25 评分：k1=1.2, b=0.75 起步；IDF 从文档频率算
- [ ] ingest 侧双写：chunk 入库时同步写倒排（WriteBatch 原子，与向量索引同事务语义）
- [ ] retrieve 侧并行：BM25 TopK ∥ HNSW TopK → 复用 `FuseHitsByRRF` 融合
- [ ] 配置：`RAG_ENABLE_BM25`（默认 true）
- [ ] 删除项：`rag_index/demo.idx` 移出仓库 + .gitignore；rerank_lexical 降级路径标记 deprecated（保留一个版本周期）
- [ ] delete/range 语义对齐：文档删除时倒排同步清（参考 e5_delete_range_test 模式）

验收（E2E）：
```bash
make run-all
# ingest 含精确关键词的文档（BM25 应赢过向量）→ retrieve 断言该 chunk 排第一
# make rag-eval → Recall@K 相比 M0 baseline 提升（预期 +10% 以上量级）
```

### M2：Router 查询路由（~1 天）

**目标**：路由决策显式化，为 L3 铺路，本身就是吞吐/成本优化。

任务：
- [ ] `router.go`：v1 纯规则——①精确命中 wiki 页标题/slug → L1；②短查询+关键词命中（BM25 高分）→ L2 单发；③长查询/比较型/多实体 → L3
- [ ] 决策结果写入查询日志（JSON），含 `route=L1/L2/L3 + reason`
- [ ] metrics：`rag_route_total{path}` 计数器
- [ ] `RAG_ENABLE_ROUTER`（默认 false，先观察日志再切默认）
- [ ] 端点不变：Router 内嵌在现有 retrieve/chat 入口，`?debug=route` 返回决策详情

验收（E2E）：
```bash
# ①命中 wiki 标题的查询 → 响应含 wiki 来源标记；②普通查询 → L2 路径日志；③规则命中 L3 时（M3 前降级走 L2）
make rag-eval → 不回退（Router 只分流不改结果质量）
```

### M3：Agentic 循环（~2~3 天）

**目标**：L3 路径真实落地，带硬预算，不做无预算的"agent 玩具"。

任务：
- [ ] `agentic.go`：状态机 PLAN → RETRIEVE → GRADE → (REWRITE → RETRIEVE) ×N → GENERATE，N≤2
- [ ] grader：v1 规则版（命中数 / 引用覆盖度 / 跨 chunk 分数阈值），`RAG_AGENTIC_GRADER=rule|llm` 可切 LLM 打分
- [ ] 硬预算：`RAG_AGENTIC_MAX_ROUNDS=2` + token 上限计数，超限降级返回 L2 结果并标记 `degraded=true`
- [ ] 查询分解：多实体查询拆成子查询（复用 multi-query 展开）
- [ ] 响应结构：`rounds` / `tokens_used` / `citations` / `degraded` 字段
- [ ] metrics + 查询日志全量记录
- [ ] 单测：预算超限降级路径、grader 拒绝路径、正常两轮路径

验收（E2E）：
```bash
# 构造一个单发检索必失败的多跳问题 → 断言响应 rounds≥2 且 citations 含两个 chunk
# 断言预算耗尽场景返回 degraded=true 且不 panic
make rag-eval → 不回退；golden 中多跳子集单独出分
```

### M4：faithfulness 评估进 CI（~1 天）

**目标**：答案质量可度量，防幻觉回退。

任务：
- [ ] `eval_faith.go`：v1 纯规则——答案句引用覆盖率（答案中关键实体/数字是否出现在引用 chunk 中）
- [ ] v2 可选：`RAG_EVAL_FAITH=llm` 时 LLM-as-judge（ChatProvider 已有）
- [ ] golden.json 扩展：每条加期望答案要点
- [ ] CI：`make rag-eval` 输出 Recall@K / MRR / faithfulness 三指标，低于阈值 fail（阈值取 baseline×0.95）
- [ ] 快照对比：每次跑完追加 `docs/rag-eval-baseline.md`（git 管质量趋势）

验收（E2E）：
```bash
make rag-eval   # 三指标齐出，故意注入一条坏答案 → CI 门禁能拦
```

### M5：Wiki 审计与回滚（~1 天，可与 M6 并行）——对应 H9 隐性需求

任务：
- [x] WikiStore 版本化：compile 时保留上一版页面（minikv key 加版本段），`RAG_WIKI_KEEP_VERSIONS=3`
- [x] `POST /api/rag/collections/:col/wiki/pages/:slug/rollback` 回滚端点（body `{"version":0}` = 撤销最近一次覆盖）
- [x] 审计：`GET .../wiki/audit` 全页引用支撑度报告（复用 M4 faithfulness 规则，零 LLM；低于 0.6 页面点名句子+排序置顶，审计动作落 wiki log）

验收（E2E，`scripts/e2e_rag_wiki.sh`）：compile → minikv 原生协议直写幻觉页（Raft/Kafka 虚构断言）→ rollback → 断言内容恢复为 v1 → audit 报告 faithfulness=1.0。**全部 PASS**。

### M6：文档收口（~0.5 天）

- [ ] RagKv.md 升 v3：完整分层图 + "为什么不用 Qdrant/LangGraph" 三问三答（H10）
- [ ] RAG-ARCHITECTURE.md 同步新数据流
- [ ] README 一段电梯陈述更新

---

## 5. 里程碑与总量

| 里程碑 | 内容 | 工作量 | 累计 |
|---|---|---|---|
| **MS1** | M0 + M1（评估基线 + BM25） | ~3 天 | 质量提升可量化 |
| **MS2** | M2 + M3（Router + Agentic） | ~3~4 天 | 三层架构成型 |
| **MS3** | M4 + M5 + M6（评估闭环 + 审计 + 文档） | ~2.5 天 | 生产化收口 |

总计 **8~9.5 天**（含 E2E 调试缓冲）。每完成一个模块 git commit 一次，格式 `rag(mN): ...`。

## 6. 风险与规避

| 风险 | 规避 |
|---|---|
| BM25 倒排膨胀（minikv 写放大） | posting list 用 delta+varint 编码；term 停用词过滤；先量测 chunk 量级再定 |
| 中文分词质量差拖累 BM25 | bigram 起步，eval 对比决定是否上 jieba 类方案（保持纯 Go：可用 Go 移植版） |
| Agentic 在 LLM 不可用时挂死 | ChatProvider 缺失 → Router 强制 L2，超时 context 控制 |
| Router 规则误路由 | 默认 off 观察一周日志，再开；`?debug=route` 可人工核查 |
| eval 指标波动（LLM 非确定性） | faithfulness LLM-judge 设 temperature=0；阈值留 5% 容差 |

---

## 7. 明确不做（本计划外）

- ❌ 引入 Qdrant/Milvus 替换 HNSW
- ❌ 引入 LangGraph/LlamaIndex（Python sidecar）
- ❌ GraphRAG 全量图谱（仅在文档中作为演进方向提及）
- ❌ web 控制台 RAG 可视化（列为 P5 待选，非本计划范围）
