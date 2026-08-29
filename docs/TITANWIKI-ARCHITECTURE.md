# TitanWiki Architecture

> 日期：2026-08-29  
> 仓：`/mnt/d/WSL/TitanKV`（`feat/industrialization`）  
> 模块：`services/rag` + MiniKV  
> 口径：分片 = Chunking(切块)，不是分布式 Sharding

---

## 1. 一句话定位

普通 RAG 每次从原文向量「重新发现」知识。  
**TitanWiki** 先把源材料编译成可交叉引用的 wiki 页与边表；问答优先走已沉淀知识，向量检索只做兜底。

---

## 2. 链路站位

```text
Client / Web
  → skynet :8080
  → Gin :18080 (JWT / RBAC)
  → RAG :8085
       ├─ 现有: Ingest → Chunk → Embed → WriteBatch(minikv) → HNSW → Chat SSE
       └─ TitanWiki:
            raw 不可变 → Compile Worker → wiki page + edge
            Query: wiki/entity 命中 → (不足) retrieve 向量兜底 → Chat + citation
  → MiniKVClient TCP → minikv :8888
  → 向量旁路：RAM + {IndexDir}/{col}.idx
```

**三原则：**
1. 向量不进 minikv  
2. 正文 / wiki / edge 用 WriteBatch 原子写  
3. 删除：KV + 内存索引 + 快照（+ wiki 边）一致

---

## 3. Key 空间

| Key | 内容 |
|-----|------|
| `wiki:raw:{col}:{src_id}` | 不可变原文 + sha256 |
| `wiki:page:{col}:{slug}` | WikiPage JSON |
| `wiki:edge:{col}:{from}:{to}` | WikiEdge JSON |
| `wiki:index:{col}` | 目录（index.md 类比） |
| `wiki:log:{col}:{ts}:{id}` | append-only 审计 |
| `wiki:task:{task_id}` | CompileTask |
| 现有 `rag:chunk:*` | 不变；wiki 摘要向量 id = `{col}/wiki/{slug}` |

---

## 4. 编译与冲突（W1 + W2.2）

1. 从 `rag:chunk` 拼 markdown → 规则 Extract（heading→slug、`[[wikilink]]`→边）  
2. 可选 `RAG_WIKI_LLM=true` 用 ChatProvider 重写 Summary  
3. **ResolveContested**：同 slug、不同 source、内容指纹不同 → 保留原页并标 `contested`，新内容写到 `slug-alt-{id}`，双向 `contradicts` 边  
4. WriteBatch 落页/边/index；Summary Embed 进 SideIndex  
5. HTTP：`POST .../wiki/compile` → 202 + task_id（异步 CompilePool）

---

## 5. 查询路径

```text
POST .../wiki/ask 或 ChatOrchestrator.AskWiki
  → WikiQuerier.Resolve（slug/title + 邻居 depth≤1）
  → 不足则 Retriever.Retrieve（向量）
  → assembleWikiPrompt → StreamComplete SSE
  → citation: wiki:{slug} 或 doc_id
```

Graph API：`GET .../wiki/graph?slug=&depth=1|2` → `BuildWikiGraph`（out/in + neighbors）  
Contested：`GET .../wiki/contested` → `ListContested`

---

## 6. 删除与 Range Delete（E5）

- 删文档：`DeleteBySource` + 按 slug 清 `{col}/wiki/{slug}` 向量 + 快照重写  
- 删整库：`Store.DeleteCollection` / `WikiStore.DeleteCollection` → 前缀 `DeleteRange`  
- 引擎：`DB::deleteRange` 分批 ≤10000 点删 tombstone（**不是** RocksDB MemTable range tombstone）

---

## 7. Web

- 路由：`/dashboard/rag/[col]/wiki`  
- 能力：index 列表、页正文、graph depth≤2、contested 列表、触发整库编译  
- 入口：知识库详情页「TitanWiki」按钮

---

## 8. 环境变量（Wiki 相关）

```bash
RAG_ENABLE_WIKI=true
RAG_WIKI_LLM=false          # true 才走 Chat 摘要
RAG_WIKI_AUTO_COMPILE=true  # ingest success 后自动 enqueue
RAG_WIKI_WORKERS=2
RAG_WIKI_QUEUE_SIZE=64
```

---

## 9. 面试口述版（约 30 秒）

「我们不是又做了一个 ChatPDF。源材料入库后，后台异步编译成 wiki 页和边表，写进 MiniKV；问答先查已编译知识，向量只做兜底。冲突来源不覆盖原文，标 contested 并留 alt 页。删文档时 KV、wiki 边、向量、快照四处一致；整库前缀删走引擎 DeleteRange 分批点删。」

---

## 10. 诚实边界

| 勿吹 | 实际 |
|------|------|
| 全自动知识图谱 / OWL | 规则抽实体+边；contested 仅标记 |
| 生产级 LLM 摘要 | 默认规则 summary；LLM 可关 |
| RocksDB range tombstone | 分批点删 +≤10000/batch |
| hash embed 高质量召回 | demo 用 local hash；质量靠 openai embed |
| 简历写 Raft | 用户既定不做 |

---

## 11. 里程碑状态（2026-08-29）

| 项 | 状态 |
|----|------|
| W0 异步 CompilePool | ✅ |
| W1 wiki keys + ask wiki-first | ✅ |
| E5 Range Delete（分批点删） | ✅ |
| W2 graph depth≤2 + contested | ✅ |
| Phase O 诚实指标 | ✅ |
| E1 Compaction L1→L2 测 | ✅ |
| E2 kBatch bench | ✅ |
| E3 BlockCache hit/miss | ✅ |
| E4 compaction retry/backoff | ✅ |
| Phase F data_backend / 任务态 / CLI wiki | ✅ |

档位：**演示级落地**（校招/作品集可端到端演示），非企业知识库成品。

---

## 12. 相关文件

`wiki_types.go` `wiki_store.go` `wiki_extract.go` `wiki_compile.go` `wiki_query.go` `wiki_handler.go`  
`scripts/smoke_wiki.sh`  
`web/app/dashboard/rag/[col]/wiki/page.tsx`  
`client-cli/main.go`（`wiki pages` / `wiki ask`）  
设计稿：`~/.hermes/plans/2026-08-29_104402-titanwiki-and-full-roadmap.md`
