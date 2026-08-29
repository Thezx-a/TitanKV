# TitanKV × RAG — 架构层级图与项目讲解

> 版本：v1.1
> 状态：已落地（随 `make run-all` 启动，默认 :8085）
> 关联方案：[`RagKv.md`](../RagKv.md) (v2.0)
> 仓库根：`TitanKV`（历史名 SpectrumCore 已废弃）
> 编译验证：`go build ./services/... ./gateway/...` 通过；`tsc --noEmit` 通过

本文档面向"已读 RagKv.md 但想看实际落地版"的读者，包含：架构层级图、各层职责、关键代码定位、数据流、KV Key 设计、接口契约、权限模型、启动流程、面试口述要点。

---

## 1. 项目定位

**TitanKV × RAG** 是在已有四层栈 (minikv / Go 服务 / skynet 网关 / Next.js 控制台) 之上，新增一个 **RAG 知识库服务 (services/rag)**，复用 minikv 作为持久化底座，构建"文档入库 → 向量检索 → LLM 流式问答"闭环。

核心原则（与方案一致）：
- **不引入第二套存储**：向量检索用 side index (内存 + 快照)，文本/chunk/会话/任务全部走 minikv TCP。
- **不改动 minikv 协议/API**：RAG 只通过现有 TCP Put/Get/Delete/Scan 消费引擎；引擎侧已有的 BlockCache / MemTable `shared_ptr` 等升级对 RAG 透明。
- **不另起权限体系**：扩展原有 RBAC，新增 `rag:ingest` / `rag:query` 两个权限。

---

## 2. 架构层级图

> 架构图：[`docs/layers/architecture.svg`](layers/architecture.svg)（最新）· 分层详解 [`docs/layers/index.html`](layers/index.html) · 示意 PNG：[`RAG-ARCHITECTURE.png`](RAG-ARCHITECTURE.png) / [`SKYNET-DETAIL.png`](SKYNET-DETAIL.png)（若滞后以 Mermaid/源码为准）

### 2.1 系统层级图 (Mermaid)

```mermaid
flowchart TB
    subgraph Client["客户端层"]
        Web["Next.js 控制台<br/(:3000)<br/>app/dashboard/rag/*"]
    end

    subgraph Edge["接入层"]
        Skynet["skynet 前置 :8080<br/>epoll ET + C++20 协程主从 Reactor"]
    end

    subgraph Gateway["API 网关层 (Go Gin :18080 / :8080)"]
        MW["中间件洋葱链<br/>RequestID → Logger → Recover → RateLimit → Auth → RBAC"]
        Proxy["ReverseProxy<br/>/api/data /api/meta /api/observability /api/rag"]
    end

    subgraph Services["业务服务层 (Go)"]
        Auth["services/auth<br/>JWT + RBAC + APIKey"]
        Meta["services/meta<br/>Collection CRUD<br/>(含 type=rag 配置)"]
        Data["services/data<br/>KV HTTP + SSE Scan"]
        Rag["services/rag :8085<br/>ingest / retrieve / chat"]
        Observ["services/observability<br/>metrics + tracing"]
    end

    subgraph Storage["存储层"]
        MiniKVServer["minikv_server<br/>(TCP :8888)"]
        MiniKV["minikv 引擎<br/>WAL / MemTable / SST<br/>BloomFilter / LRU / snappy"]
        SideIndex["向量 side index<br/>默认 HNSW（可 brute）<br/>+ 本地快照文件"]
    end

    Web -->|HTTP / SSE| Skynet
    Web -.->|HTTP / SSE 直连| Gateway
    Skynet --> Gateway
    Gateway --> MW --> Proxy
    Proxy --> Auth
    Proxy --> Meta
    Proxy --> Data
    Proxy --> Rag
    Proxy --> Observ

    Rag -->|Go TCP (MiniKVClient)| MiniKVServer
    Data -->|Go TCP (MiniKVClient)| MiniKVServer
    MiniKVServer --> MiniKV
    Rag <==> SideIndex
```

### 2.2 落地版 ASCII 层级图

```
┌────────────────────────────────────────────────────────────────────┐
│  Client / Web (Next.js :3000)                                       │
│  app/dashboard/rag/{page, [col]/page, [col]/chat/page}.tsx         │
└──────────────────────────────┬─────────────────────────────────────┘
                               │ HTTP / SSE (Bearer JWT)
┌──────────────────────────────▼─────────────────────────────────────┐
│  Gateway：对外 skynet :8080 → Gin :18080（run-all）/ 单独 Gin :8080   │
│  中间件链: RequestID → Logger → Recover → RateLimit → Auth → RBAC  │
│  反向代理分组:                                                     │
│    /api/data    → DataServiceURL                                   │
│    /api/meta    → MetaServiceURL   (Collection CRUD, 含 rag_config)│
│    /api/observability → ObservURL                                  │
│    /api/rag     → RAGServiceURL    (rag:ingest / rag:query 区分)   │
└──┬──────────────┬──────────────┬──────────────┬────────────────────┘
   │              │              │              │
   ▼              ▼              ▼              ▼
┌────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────────────────────┐
│ auth   │  │ meta     │  │ data     │  │ services/rag :8085          │
│ JWT/RBAC│ │ Collection│ │ KV HTTP  │  │  ingest │ retrieve │ chat    │
│        │  │ +rag_cfg │  │ +SSE Scan│  │  ↓ Go TCP (MiniKVClient)     │
└────────┘  └──────────┘  └──────────┘  └────────────┬────────────────┘
                                                          │
                                ┌─────────────────────────▼──────────┐
                                │   minikv_server TCP (:8888)        │
                                │   LSM + BlockCache + 主从 Reactor │
                                └─────────────────┬──────────────────┘
                                                  │ C++ DB::open/put/write/Iterator
                                ┌─────────────────▼──────────────────┐
                                │          minikv 引擎               │
                                │   WAL / MemTable / SSTable         │
                                │   BloomFilter / LRU Cache          │
                                │   snappy 压缩 / WriteBatch 原子写  │
                                └────────────────────────────────────┘

                                ┌────────────────────────────────────┐
                                │  Vector Side Index (rag 进程内)    │
                                │  默认 HNSW（可 brute）；chunk_id→vec │
                                │  内存索引 + 本地快照文件            │
                                └────────────────────────────────────┘
```

### 2.3 services/rag 内部模块图

```
services/rag/
├── cmd/main.go          ← 启动 :8085，gin.New() + RegisterRoutes
├── config.go            ← LoadConfig() 从 env 读取 (RAG_ADDR/MINIKV_ADDR/OPENAI_*)
├── handler.go           ← HTTP 路由 + Service 注册 (IngestDocument/List/Retrieve/Chat/...)
├── store.go             ← minikv 客户端封装 + RAG Key 编码 + 文档 CRUD
├── vector_index.go      ← SideIndex 抽象 + brute cosine TopK + 快照
├── hnsw_index.go        ← 默认向量索引 (RAG_INDEX_TYPE=hnsw)
├── rerank.go            ← 可选 rerank
├── eval.go / eval_test  ← 检索评测辅助
├── embedding.go         ← Embedding Provider 抽象 (Hash mock + OpenAI) + 缓存
├── chunker.go           ← 固定窗口切块 (按字符/token，可配 chunk_size + overlap)
├── ingest.go            ← 文档入库编排 (清洗/去重/切块/embed/持久化/更新索引)
├── retrieve.go          ← 检索编排 (query embed → side index TopK → 回查 chunk 文本)
├── llm.go               ← ChatProvider 抽象 (mock 流式 + OpenAI 流式)
├── chat.go              ← 问答编排 (retrieve → prompt 三段式 → LLM → SSE)
└── (Service)            ← Service struct 聚合 store/idx/emb/llm, 提供 Close()
```

---

## 3. 各层职责与代码定位

### 3.1 存储层：minikv (C++)

**职责**：所有 RAG 文本/chunk/会话/任务的持久化。**不存向量**，向量进 side index。
**复用方式**：rag 服务通过 `services/data.MiniKVClient` 同款 TCP 协议 (`Put/Get/Delete/Scan`) 访问。
**引擎侧**：RAG 不直接改 C++ 代码；持久化复用 Iterator Seek 前缀扫描 + WriteBatch。minikv 已落地 BlockCache（解压后 data block LRU）与 MemTable `shared_ptr` 锁外读，对 RAG 透明。

### 3.2 业务服务层：services/rag (Go)

**职责**：把"文档 → embedding → chunk 入库 → 检索 → 流式问答"全流程编排起来。
**入口**：[services/rag/cmd/main.go](file://../services/rag/cmd/main.go)
**路由注册**：[services/rag/handler.go](file://../services/rag/handler.go) `RegisterRoutes`

| 路由 | Handler | 权限 | 说明 |
|---|---|---|---|
| `GET /healthz` | inline | 无 | 健康检查 |
| `POST /api/rag/collections/:col/documents` | `IngestDocument` | `rag:ingest` | multipart file \| json text 入库 |
| `GET /api/rag/collections/:col/documents` | `ListDocuments` | `rag:query` | 文档列表 (Iterator 前缀扫描) |
| `GET /api/rag/collections/:col/documents/:doc` | `GetDocument` | `rag:query` | 文档详情含 chunks |
| `DELETE /api/rag/collections/:col/documents/:doc` | `DeleteDocument` | `rag:ingest` | 级联删 chunks/meta/status |
| `POST /api/rag/collections/:col/retrieve` | `Retrieve` | `rag:query` | 检索 + 返回 hits |
| `POST /api/rag/collections/:col/chat` | `Chat` | `rag:query` | SSE 流式问答 |
| `GET /api/rag/tasks/:task_id` | `GetTask` | `rag:query` | 入库任务状态 |
| `GET /api/rag/index/snapshot` | `SaveSnapshot` | `rag:ingest` | 手动触发索引快照 |

### 3.3 网关层：gateway (Go Gin)

**职责**：统一鉴权、限流、RBAC、反向代理。
**关键扩展点**：[gateway/router.go](file://../gateway/router.go)
- `Config` 新增 `RAGServiceURL`，env `RAG_SERVICE_URL`，默认 `http://localhost:8085`
- 反向代理 map 加入 `"/api/rag": cfg.RAGServiceURL`
- 新增中间件 `ragRBAC`：
  - 路径以 `/retrieve` 或 `/chat` 结尾 → `rag:query`（即使是 POST 也算读）
  - GET / HEAD / OPTIONS → `rag:query`
  - POST / PUT / PATCH / DELETE → `rag:ingest`
- `/api/rag` 单独成 Group，挂 `authMW + ragRBAC`，避免与 KV 的方法映射混淆

### 3.4 元数据层：services/meta (Go)

**职责**：管理 Collection 元数据，扩展支持 `type=rag`。
**关键扩展点**：[services/meta/store.go](file://../services/meta/store.go)
- `Collection` 新增字段：`Type string` (`"kv"` | `"rag"`) 和 `RagConfig *RagCollectionConfig`
- `RagCollectionConfig` 字段：embedding_model / embedding_dim / distance_metric / chunk_size / chunk_overlap / top_k_default / rerank_enabled / allowed_file_types / max_doc_size_mb / owner / visibility
- `validateCollection` 校验 type 与 rag_config 一致：`type=rag` 必须带 rag_config；`type=kv` 不允许带 rag_config
- `Update` 增加 rag 参数：`Update(name, ttl, schema, rag)`，type 创建后不可变

### 3.5 前端：web (Next.js 14 App Router)

**职责**：知识库管理 + 流式问答 UI。
| 文件 | 作用 |
|---|---|
| [web/lib/types.ts](file://../web/lib/types.ts) | 扩展 `Collection.type`、新增 `RagCollectionConfig` / `DocumentMeta` / `ChunkRecord` / `IngestTask` / `RetrievalHit` / `ChatEvent` 等 |
| [web/lib/rag.ts](file://../web/lib/rag.ts) | `ragApi` 封装：uploadDocument / ingestText / listDocuments / getDocument / deleteDocument / getTask / retrieve / chatStream (AsyncGenerator) |
| [web/components/nav.tsx](file://../web/components/nav.tsx) | 侧边栏加 "知识库 (RG)" 入口 → `/dashboard/rag` |
| [web/app/dashboard/rag/page.tsx](file://../web/app/dashboard/rag/page.tsx) | 知识库列表 + 创建表单 (type=rag) |
| [web/app/dashboard/rag/\[col\]/page.tsx](file://../web/app/dashboard/rag/[col]/page.tsx) | 知识库详情：文档列表 + 上传 + 文本入库 + 删除 |
| [web/app/dashboard/rag/\[col\]/chat/page.tsx](file://../web/app/dashboard/rag/[col]/chat/page.tsx) | 流式问答页：原生 fetch + ReadableStream 解析 SSE，渲染 token/citation/end/error |

**SSE 实现要点**：`ragApi.chatStream` 用 `async function*` 生成器消费 fetch 流，逐行解析 `event: xxx` / `data: {...}`，绕开 `EventSource`（不支持 POST + 自定义 Header）。

---

## 4. 数据流详解

### 4.1 文档入库流程

```
[Web 上传文件]
   │ POST multipart /api/rag/collections/:col/documents
   ▼
[Gateway] authMW → ragRBAC(rag:ingest) → ReverseProxy
   ▼
[rag.IngestDocument]
   │ 1. 解析 multipart / json
   │ 2. contentHash = sha256(content)
   │ 3. store.GetDocumentByHash() 去重 (WriteBatch 原子读)
   │ 4. chunker.Split(content, chunk_size, overlap) → []Chunk
   ▼
[embedding.EmbedBatch]
   │ 5. 每块 chunk → 向量 (provider=hash|openai)
   │    - Hash provider: 离线 mock，无需 API Key
   │    - OpenAI provider: 走 OPENAI_API_KEY + 缓存 (rag:cache:emb:{hash})
   ▼
[store.SaveDocument]
   │ 6. WriteBatch 原子写:
   │      rag:doc:{col}:{doc}:meta   = DocumentMeta JSON
   │      rag:doc:{col}:{doc}:status = "success"
   │      rag:chunk:{col}:{doc}:{seq:08d} = ChunkRecord JSON  (× N)
   │      rag:col:{col}:stats        = 更新后的统计
   ▼
[SideIndex.Add]
   │ 7. 内存 index[col][chunk_id] = vector
   │ 8. 触发定时 / 手动 SaveSnapshotPrefix(col)
   ▼
[Response] { task_id, doc_id, estimated_chunks }
```

**原子性保证**：WriteBatch 一次系统调用，WAL 一次 fsync，崩溃恢复后要么全有要么全无，不存在"doc 状态 success 但 chunk 缺失"中间态。

### 4.2 检索流程

```
[Web 检索请求]
   │ POST /api/rag/collections/:col/retrieve  body: { query, top_k, debug }
   ▼
[rag.Retrieve]
   │ 1. emb.Embed(query) → query_vec
   │ 2. idx.TopK(query_vec, top_k) → []Hit (chunk_id, score)
   │ 3. for each hit: store.GetChunk(chunk_id) → 回查 chunk 文本
   │ 4. (可选) rerank stub — 第一阶段未启用
   ▼
[Response] { hits: [{chunk_id, score, text, doc_id, heading, page_num}], rewrite_query?, latency_ms }
```

**关键点**：向量检索在 rag 进程内存完成 (cosine 暴力 TopK，第一阶段文档量小够用)；命中 chunk_id 后回查 minikv 拿文本，避免向量也进 KV。

### 4.3 流式问答流程 (SSE)

```
[Web chat 页]
   │ POST /api/rag/collections/:col/chat  body: { query, session_id, top_k, history_turns }
   │ Accept: text/event-stream
   ▼
[rag.Chat]
   │ 1. Retrieve(query, top_k) → hits
   │ 2. prompt.Build(system, context=hits.text, user=query, history=last N turns)
   │ 3. c.Stream(func(w)) 循环:
   │      llm.Stream(prompt) → token
   │        → SSE: event: token\ndata: {"text":"..."}
   │      每个 citation:
   │        → SSE: event: citation\ndata: {chunk_id, doc_id, score, ...}
   │      debug: event: debug\ndata: {rewrite_query, top_k, prompt_tokens}
   │      结束: event: end\ndata: {total_tokens, latency_ms}
   │      异常: event: error\ndata: {code, msg}
   ▼
[Web] ragApi.chatStream (AsyncGenerator) 逐事件 yield, UI 增量渲染
```

---

## 5. KV Key 空间设计 (与方案 §4 一致)

```
rag:col:{col}:cfg                       → RagCollectionConfig JSON
rag:col:{col}:stats                     → 文档数/chunk 数/最后更新时间
rag:doc:{col}:{doc_id}:meta             → DocumentMeta JSON
rag:doc:{col}:{doc_id}:status           → "pending"|"running"|"success"|"failed"
rag:chunk:{col}:{doc_id}:{seq:08d}      → ChunkRecord JSON (正文+元数据)
rag:task:{task_id}                      → IngestTask JSON
rag:cache:emb:{hash}                    → EmbeddingCache JSON (向量缓存)
rag:index:{col}:version                 → uint64 版本号
rag:index:{col}:snap:{ver:016d}         → side index 快照路径
rag:qlog:{col}:{req_id}                 → QueryLog JSON
rag:chat:{uid}:{sid}:{seq:08d}          → ChatMessage JSON (会话历史)
```

**前缀扫描用法** (services/rag/store.go)：

```go
// 列出某知识库下所有文档 (Iterator seek 一次定位，流式遍历整个前缀)
prefix := fmt.Sprintf("rag:doc:%s:", colName)
iter := client.Scan(prefix)  // 复用 data.MiniKVClient.Scan
for iter.Next() {
    var meta DocumentMeta
    json.Unmarshal([]byte(iter.Value()), &meta)
    // ...
}

// 取某文档所有 chunk (按 seq 顺序，Iterator 天然排序)
prefix := fmt.Sprintf("rag:chunk:%s:%s:", colName, docID)
```

**WriteBatch 入库事务**：

```go
// 单文档全量入库 — 原子提交
batch := minikv.NewWriteBatch()
for _, chunk := range chunks {
    batch.Put(chunkKey(col, docID, chunk.Seq), marshalChunk(chunk))
}
batch.Put(docMetaKey(col, docID), marshalDocMeta(doc))
batch.Put(docStatusKey(col, docID), "success")
batch.Put(colStatsKey(col), marshalStats(updatedStats))
client.Write(batch)  // 一次系统调用，WAL 一次 fsync
```

---

## 6. 接口契约

### 6.1 知识库管理 (扩展 meta)

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/meta/collections` | body: `{ type:"rag", name, rag_config:{...} }` |
| GET | `/api/meta/collections/:name` | 返回含 `rag_config` |
| PUT | `/api/meta/collections/:name` | 可改 `top_k_default` / `rerank_enabled` / `visibility` |
| DELETE | `/api/meta/collections/:name` | 级联删 `rag:col:` 前缀所有 key |

### 6.2 文档入库 (rag)

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/rag/collections/:col/documents` | multipart file \| json text |
| GET | `/api/rag/collections/:col/documents` | 文档列表 |
| GET | `/api/rag/collections/:col/documents/:doc` | 详情含 chunks |
| DELETE | `/api/rag/collections/:col/documents/:doc` | 级联删 chunks/meta/status |
| GET | `/api/rag/tasks/:task_id` | 入库任务状态 |

### 6.3 检索与问答 (rag)

| Method | Path | 说明 |
|---|---|---|
| POST | `/api/rag/collections/:col/retrieve` | body: `{query, top_k, filters, debug}` |
| POST | `/api/rag/collections/:col/chat` | SSE: `token` / `citation` / `debug` / `end` / `error` |

---

## 7. 权限模型 (RBAC)

### 7.1 新增权限

[gateway/middleware/rbac.go](file://../gateway/middleware/rbac.go) 新增：

```go
PermRAGIngest Permission = "rag:ingest" // 文档入库 / 删除 / 索引快照
PermRAGQuery  Permission = "rag:query"  // 检索 / 流式问答 / 文档列表
```

### 7.2 角色权限矩阵

| 角色 | kv:get | kv:put | kv:delete | collection:create | rag:ingest | rag:query |
|---|---|---|---|---|---|---|
| admin | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| writer | ✓ | ✓ | - | ✓ | ✓ | ✓ |
| reader | ✓ | - | - | - | - | ✓ |

### 7.3 Gateway 路由到权限映射

- `/api/data`, `/api/meta`, `/api/observability` → `kvRBAC` 按 HTTP 方法映射 `kv:get/put/delete`
- `/api/rag` → `ragRBAC`：
  - `*/retrieve`, `*/chat` → `rag:query`（POST 也算读）
  - `GET/HEAD/OPTIONS` → `rag:query`
  - `POST/PUT/PATCH/DELETE` → `rag:ingest`

---

## 8. 启动与部署

### 8.1 依赖服务 (deploy/dev/docker-compose.yml)

`docker compose -f deploy/dev/docker-compose.yml up -d` 拉起：postgres / redis / etcd / jaeger / prometheus / grafana。

### 8.2 启动顺序

```bash
# 1. minikv_server (C++ TCP :8888)
./build-minikv/minikv_server --addr 127.0.0.1:8888 --db ./minikv-data

# 2. meta service :8083
go run ./services/meta/cmd

# 3. rag service :8085
MINIKV_ADDR=127.0.0.1:8888 \
RAG_ADDR=:8085 \
RAG_INDEX_DIR=./rag_index \
go run ./services/rag/cmd
# 无 OPENAI_* 时自动降级为 hash embedding + mock chat, 保证可独立运行

# 4. gateway :8080
RAG_SERVICE_URL=http://localhost:8085 \
go run ./gateway/cmd

# 5. web :3000
cd web && npm run dev
# 浏览器访问 http://localhost:3000/dashboard/rag
```

### 8.3 关键环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `RAG_ADDR` (或 `RAG_SERVICE_PORT`) | `:8085` | rag 服务监听地址 |
| `MINIKV_ADDR` | `127.0.0.1:8888` | minikv TCP 地址 |
| `RAG_INDEX_DIR` | `./rag_index` | 向量快照目录 |
| `RAG_EMBEDDING_PROVIDER` | `hash` | `hash` \| `openai` |
| `OPENAI_API_KEY` | - | openai provider 必填 |
| `OPENAI_EMBEDDING_MODEL` | `text-embedding-3-small` | |
| `OPENAI_CHAT_MODEL` | `gpt-4o-mini` | |
| `RAG_SERVICE_URL` | `http://localhost:8085` | gateway 反代目标 |

---

## 9. 面试口述要点

> 这套 TitanKV × RAG 的核心是"**让 minikv 既当 KV 又当 RAG 的持久化底座**"。
>
> **为什么 minikv 适合做 RAG 底座**：Iterator `seek(prefix)` 让 chunk 列表遍历 O(1) 定位；`WriteBatch` 原子写保障了文档入库的一致性，崩溃恢复后要么全有要么全无；BloomFilter + LRU Cache 加速了 retrieval 热路径；snappy 压缩把大量文本 chunk 的存储成本压到原来的 1/3。
>
> **为什么向量不进 minikv**：向量是 float32 数组，压缩率低、点查为主，放 KV 性价比差。所以第一阶段做"**向量旁路索引**"——side index 在 rag 进程内存维护 (chunk_id → float32[])，定时/手动快照到本地文件，重启时合并快照恢复。文本 chunk 走 minikv + snappy，两者通过 `chunk_id` 关联。
>
> **架构分层**：客户端 (Next.js) → Gateway (Gin, 中间件洋葱链 + RBAC) → 业务服务 (auth/meta/data/rag) → minikv (TCP) → 引擎。Gateway 对 `/api/rag` 单独定义 RBAC：`rag:ingest` 管写、`rag:query` 管读 + 检索 + 问答，`/retrieve` 和 `/chat` 虽是 POST 但归 query 权限。
>
> **数据流**：入库 = 解析 → sha256 去重 → 固定窗口切块 → embed → WriteBatch 原子写 (chunks + meta + status + stats) → 更新 side index；检索 = query embed → side index TopK (cosine 暴力) → 回查 minikv 拿 chunk 文本；问答 = retrieve → prompt 三段式 → LLM 流式 → SSE (token/citation/end/error)。
>
> **可独立运行**：没配 `OPENAI_*` 时降级为 hash embedding + mock chat，整套闭环在本地一条命令就能跑通，方便演示。

---

## 10. 已完成清单

- [x] services/rag 完整实现 (12 文件)：config / store / vector_index / embedding / chunker / ingest / retrieve / llm / chat / handler + cmd/main.go
- [x] gateway 加 `/api/rag` 反代 + `ragRBAC` 中间件
- [x] gateway/middleware/rbac.go 加 `rag:ingest` / `rag:query` 权限并扩展角色矩阵
- [x] services/meta `Collection` 加 `type` + `rag_config`，CRUD 全链路支持 RAG
- [x] web 加 `lib/rag.ts`、`lib/types.ts` 扩展、nav 入口、3 个页面 (列表/详情/SSE 问答)
- [x] WSL 内 `go build ./services/... ./gateway/...` 通过
- [x] WSL 内 `go vet` 通过
- [x] web `npx tsc --noEmit` 通过

---

## 11. 后续可扩展项 (非本阶段范围)

- minikv 内嵌 ANN 索引 (HNSW / IVF)，向量下沉到引擎层
- 向量快照从本地文件升级到对象存储 (S3/OSS)
- 入库任务支持断点续写 + 重试 (基于 `rag:task:` 状态机)
- rerank stub 接入真实 reranker (bge-reranker)
- 多模态文档解析 (PDF OCR / 表格抽取)
- 评测体系 (`services/rag/eval.go`) + ground truth 数据集
