# TitanKV × RAG 知识库 — 深度结合 minikv 的详细实施计划

> 版本：v2.1  
> 状态：**已落地**（见 `services/rag/`、`make run-rag` / `make run-all`、落地说明 [`docs/RAG-ARCHITECTURE.md`](docs/RAG-ARCHITECTURE.md)）  
> 目标：在 TitanKV 现有四层栈上，以 minikv 的原生能力（Iterator Seek 前缀扫描 / WriteBatch 原子写 / 压缩 / BloomFilter / BlockCache）为持久化底座，构建完整可运行的 RAG 知识库闭环。本文保留为设计依据；实现细节以源码与 RAG-ARCHITECTURE 为准。

---

## 1. 为什么 minikv 适合做 RAG 底座

在深入讲架构之前，先把 minikv 和 RAG 的匹配点摆清楚，方便面试口述。

### 1.1 minikv 公开接口

```cpp
// include/minikv/db.h
class DB {
    static Status open(const Options&, std::unique_ptr<DB>*);
    Status put(WriteOptions, Slice key, Slice value);
    Status get(ReadOptions, Slice key, std::string* value);
    Status del(WriteOptions, Slice key);
    Status write(WriteOptions, WriteBatch&);       // 原子批量写
    std::unique_ptr<Iterator> newIterator(ReadOptions);
    void compact();
};

// Iterator
bool valid(); void seekToFirst(); void seek(Slice prefix);
void next(); Slice key(); Slice value();
```

```cpp
// include/minikv/options.h
struct Options {
    size_t memtable_size = 4MB;
    size_t block_size    = 4KB;
    size_t lru_cache_capacity = 8MB;
    bool   bloom_filter_enabled = true;
    double bloom_false_positive_rate = 0.01;
    uint8_t compression = 1;   // 0=none 1=snappy 2=zstd
    int     max_level   = 7;
    size_t  level0_compaction_trigger = 4;
    std::string db_path;
};
```

### 1.2 每个能力对 RAG 的价值

| minikv 能力 | RAG 用途 | 具体说明 |
|---|---|---|
| `seek(prefix)` 前缀扫描 | 文档列表/chunk 列表/会话回放 | key 按 `rag:{ns}:{col}:…` 组织后，Iterator seek 一次即可流式遍历整个前缀 |
| `WriteBatch` 原子批量写 | chunk 入库事务 | 一个文档的 N 个 chunk + doc meta + 状态 key 一次 `db.write(batch)` 原子提交，避免半写 |
| BloomFilter | chunk 点查加速 | 单 chunk 按 chunk_id 查询，直接命中 BloomFilter 跳过无关 SSTable |
| LRU Cache | 热 chunk 缓存 | 高频被召回的 chunk 留在 block cache，减少 SSTable 磁盘 IO |
| snappy/zstd 压缩 | chunk 文本存储 | 文本 chunk 压缩率高（通常 3–6x），显著降低 SSTable 体积与 IO |
| WAL + Crash Recovery | 入库任务可恢复 | 进程崩溃后，已写 WAL 的 chunk 下次重启自动恢复，入库任务可从断点续写 |
| MVCC 快照读 | 并发安全 | 多个 retrieval 并发查询时，读用快照隔离，不与后台 compaction/flush 冲突 |
| `compact()` | 知识库归档 | 知识库冷冻后手动调 compact，把碎片化 SSTable 合并，降低读放大 |

### 1.3 面试口述版（背这句就够）

> minikv 的 Iterator seek 前缀扫描让 chunk 列表遍历 O(1) 定位；WriteBatch 原子写保障了文档入库的一致性；BloomFilter + LRU Cache 加速了 retrieval 热路径；snappy 压缩把大量文本 chunk 的存储成本压到原来的 1/3。

---

## 2. 当前能力与缺口

### 2.1 已有可直接复用的能力

**存储层 minikv**
- Put/Get/Del/WriteBatch/Iterator — RAG 所需的全部存储原语都在。
- 原生 TCP server（`minikv_server`）已上线，Go `services/data/` 通过 `MINIKV_ADDR` 接入。
- WAL 保证宕机恢复，Manifest 保证重启后 SSTable 版本一致。

**Go 服务层**
- `services/data/`：KV HTTP + SSE Scan，RAG 入库与会话历史流式回放可直接复用。
- `services/meta/`：Collection CRUD，可扩展为知识库 schema 管理。
- `services/auth/`：JWT/RBAC/APIKey/Redis 限流，租户与知识库权限控制的基础全在。

**网络层**
- `skynet`：C++20 协程网关，已支持 SSE 代理，天然适合 LLM token 流式输出。
- `gateway/`：中间件洋葱链（RequestID → Logger → Recover → RateLimit → Auth → RBAC）。

**前端**
- `web/lib/api.ts` 统一 `apiFetch`，增量接入 RAG API 无需大改。

### 2.2 缺口清单

1. 没有文档导入链路（上传/解析/切块/清洗/去重/任务追踪）
2. 没有 Embedding provider 抽象（批量嵌入/重试/缓存）
3. 没有向量 side index（minikv 是 KV，ANN 不在第一阶段改引擎）
4. 没有 RAG 编排服务（`services/rag/`）
5. Collection schema 缺少 RAG 语义字段
6. 没有评测与指标体系

---

## 3. 总体架构

```
           Client / Web (Next.js :3000)
                     │ HTTP / SSE
           ┌─────────▼──────────┐
           │  skynet :8080       │  C++20 协程前置，SSE 透传 LLM token
           └─────────┬──────────┘
                     │ reverse proxy
           ┌─────────▼──────────┐
           │  Gin gateway :18080 │  JWT/RBAC/RateLimit/RequestID
           └──┬──────┬──────────┘
              │      │
    ┌──────────▼──┐ ┌─▼────────────────────────────────────────────────┐
    │services/auth│ │              services/rag :8085                   │
    │  JWT / RBAC │ │  ingest │ retrieve │ chat │ eval │ debug          │
    └─────────────┘ └──────────────────────┬──────────────────────────┘
                                           │ Go 直连 TCP
                              ┌────────────▼────────────┐
                              │   minikv_server TCP      │  （现有，不改）
                              └────────────┬────────────┘
                                           │ C++ DB::open / put / write / newIterator
                              ┌────────────▼────────────┐
                              │         minikv           │
                              │  WAL / MemTable / SST    │
                              │  BloomFilter / LRU       │
                              │  snappy 压缩             │
                              └─────────────────────────┘
                              
                              ┌────────────────────────┐
                              │  Vector Side Index       │  第一阶段：内存+定时快照到本地文件
                              │  chunk_id → float[]      │  检索完后回查 minikv 拿 chunk text
                              └────────────────────────┘
```

**关键设计决策**

- RAG 服务经现有 `minikv_client.go` TCP 协议读写 minikv，**不引入第二套存储**。
- 向量 side index 第一阶段做内存 + 本地文件快照，**不修改 minikv 核心**，避免打乱已完成的存储引擎主线。
- minikv 的 `WriteBatch` 保证每个文档入库是原子操作；`Iterator.seek` 做前缀扫描取代 SQL 查询。

---

## 4. KV Key 空间设计（核心）

这是与 minikv 深度结合的关键——所有数据用前缀化 key 组织，利用 Iterator `seek(prefix)` 流式遍历。

### 4.1 前缀规范

```
rag:col:{col_name}:cfg              → RagCollectionConfig JSON
rag:col:{col_name}:stats            → 文档数/chunk 数/最后更新时间
rag:doc:{col_name}:{doc_id}:meta    → DocumentMeta JSON
rag:doc:{col_name}:{doc_id}:status  → "pending"|"running"|"success"|"failed"
rag:chunk:{col_name}:{doc_id}:{seq:08d}  → ChunkRecord JSON（正文+元数据）
rag:task:{task_id}                  → IngestTask JSON（入库任务状态）
rag:cache:emb:{hash}                → EmbeddingCache JSON（向量缓存）
rag:index:{col_name}:version        → uint64 版本号
rag:index:{col_name}:snap:{ver:016d} → side index 快照路径
rag:qlog:{col_name}:{req_id}        → QueryLog JSON（检索+问答日志）
rag:chat:{uid}:{sid}:{seq:08d}      → ChatMessage JSON（会话历史）
```

### 4.2 Scan 用法示例

```go
// 列出某知识库下所有文档
prefix := fmt.Sprintf("rag:doc:%s:", colName)
iter := db.NewIterator()
iter.Seek(prefix)
for ; iter.Valid() && strings.HasPrefix(iter.Key(), prefix); iter.Next() {
    // iter.Value() = DocumentMeta JSON
}

// 取某文档所有 chunk（按 seq 顺序，Iterator 天然排序）
prefix := fmt.Sprintf("rag:chunk:%s:%s:", colName, docID)
iter.Seek(prefix)
for ; iter.Valid() && strings.HasPrefix(iter.Key(), prefix); iter.Next() {
    var c ChunkRecord
    json.Unmarshal([]byte(iter.Value()), &c)
    chunks = append(chunks, c)
}
```

### 4.3 WriteBatch 入库事务

```go
// 单文档全量入库 — 原子提交
batch := minikv.NewWriteBatch()
for _, chunk := range chunks {
    batch.Put(chunkKey(col, docID, chunk.Seq), marshalChunk(chunk))
}
batch.Put(docMetaKey(col, docID), marshalDocMeta(doc))
batch.Put(docStatusKey(col, docID), "success")
batch.Put(colStatsKey(col), marshalStats(updatedStats))
db.Write(batch)  // 一次系统调用，WAL 一次 fsync
```

这样的好处：
- 任意 chunk 单独写失败时，整批 batch 回滚，不存在"doc 状态 success 但 chunk 缺失"的中间态。
- 跨 chunk 的 WAL 记录在同一个 WriteBatch entry，崩溃恢复后要么全有要么全无。

---

## 5. 数据模型

### 5.1 RagCollectionConfig

```go
type RagCollectionConfig struct {
    Name             string   `json:"name"`
    Type             string   `json:"type"`            // "rag"
    EmbeddingModel   string   `json:"embedding_model"` // e.g. "bge-small-zh-v1.5"
    EmbeddingDim     int      `json:"embedding_dim"`   // e.g. 512
    DistanceMetric   string   `json:"distance_metric"` // "cosine"|"dot"|"l2"
    ChunkSize        int      `json:"chunk_size"`      // tokens, e.g. 512
    ChunkOverlap     int      `json:"chunk_overlap"`   // e.g. 64
    TopKDefault      int      `json:"top_k_default"`   // e.g. 5
    RerankEnabled    bool     `json:"rerank_enabled"`
    AllowedFileTypes []string `json:"allowed_file_types"` // ["txt","md","pdf"]
    MaxDocSizeMB     int      `json:"max_doc_size_mb"`
    Owner            string   `json:"owner"`
    Visibility       string   `json:"visibility"` // "private"|"shared"
    CreatedAt        int64    `json:"created_at"`
}
```

### 5.2 DocumentMeta

```go
type DocumentMeta struct {
    DocID        string            `json:"doc_id"`
    Collection   string            `json:"collection"`
    Title        string            `json:"title"`
    SourceType   string            `json:"source_type"` // "text"|"markdown"|"pdf"
    ContentHash  string            `json:"content_hash"` // sha256，用于去重
    ChunkCount   int               `json:"chunk_count"`
    TokenCount   int               `json:"token_count"`
    SizeBytes    int64             `json:"size_bytes"`
    CreatedAt    int64             `json:"created_at"`
    UpdatedAt    int64             `json:"updated_at"`
    Extra        map[string]string `json:"extra"`
}
```

### 5.3 ChunkRecord

```go
type ChunkRecord struct {
    ChunkID    string            `json:"chunk_id"`   // docID + seq
    DocID      string            `json:"doc_id"`
    Collection string            `json:"collection"`
    Seq        int               `json:"seq"`
    Text       string            `json:"text"`
    TokenCount int               `json:"token_count"`
    CharStart  int               `json:"char_start"`
    CharEnd    int               `json:"char_end"`
    Heading    string            `json:"heading"`    // 最近父标题
    PageNum    int               `json:"page_num"`   // PDF 页码
    Extra      map[string]string `json:"extra"`
}
```

**注意**：ChunkRecord 只存文本和元数据，**不存向量**。向量进 side index，文本进 minikv，两者通过 chunk_id 关联。这样 minikv 的 snappy 压缩可以充分压缩文本（通常 3–4x），而向量是 float32 数组，压缩率低，分开存储更合理。

### 5.4 QueryLog

```go
type QueryLog struct {
    ReqID          string        `json:"req_id"`
    Collection     string        `json:"collection"`
    Query          string        `json:"query"`
    RewriteQuery   string        `json:"rewrite_query"`
    TopK           int           `json:"top_k"`
    Hits           []RetrievalHit `json:"hits"`
    PromptTokens   int           `json:"prompt_tokens"`
    CompletTokens  int           `json:"completion_tokens"`
    LatencyMs      int64         `json:"latency_ms"`
    Timestamp      int64         `json:"timestamp"`
}
```

---

## 6. 模块拆分与目录计划

```
services/rag/
├── cmd/main.go              — 启动入口，:8085
├── config.go                — 环境变量读取
├── router.go                — Gin 路由注册
├── types.go                 — 所有公共 struct（上方数据模型）
├── store.go                 — minikv 客户端封装（Scan 前缀/WriteBatch 入库）
├── store_test.go            — 单元测试（覆盖 key 编解码、批量写、前缀扫描）
├── ingest.go                — 文档入库编排
├── chunker.go               — 文本切块（固定窗口 V1 / 标题边界 V1.5）
├── provider_embedding.go    — Embedding provider 抽象 + 远端实现
├── provider_chat.go         — ChatProvider 抽象 + 流式实现
├── vector_index.go          — Side index（内存 cosine/dot + 快照到文件）
├── retrieve.go              — 检索编排（embed → ANN → meta filter → rerank stub）
├── chat.go                  — 问答编排（retrieve → prompt → LLM → SSE）
├── prompt.go                — Prompt 拼装（system / context / user 三段式）
├── task_store.go            — 入库任务状态（读写 rag:task: 前缀）
└── eval.go                  — 评测接口（批量 retrieve 对比 ground truth）

services/meta/
└── rag_collection.go        — 扩展 Collection CRUD，支持 type=rag + RagCollectionConfig

gateway/
└── handler/rag_proxy.go     — /api/rag/* 路由代理（复用现有 proxy 结构）

web/
├── app/rag/page.tsx                    — 知识库列表
├── app/rag/[col]/page.tsx              — 知识库详情（文档列表）
├── app/rag/[col]/chat/page.tsx         — 流式问答页
├── app/rag/[col]/debug/page.tsx        — 检索调试页（topK + score 展示）
├── components/rag/
│   ├── CollectionCard.tsx
│   ├── DocumentUpload.tsx
│   ├── ChatWindow.tsx                  — SSE 消费，流式渲染
│   └── CitationPanel.tsx              — 来源 chunk 引用展示
└── lib/rag.ts                          — ragApi.uploadDocument / retrieve / chatStream

deploy/dev/
└── docker-compose.yml                  — 新增 rag 服务 env（RAG_EMBEDDING_PROVIDER 等）
```

---

## 7. 接口设计

### 7.1 知识库管理（扩展 meta）

```
POST   /api/meta/collections          body: { type:"rag", ...RagCollectionConfig }
GET    /api/meta/collections/:name    返回含 RagCollectionConfig
PUT    /api/meta/collections/:name    可改 top_k_default / rerank_enabled / visibility
DELETE /api/meta/collections/:name    级联删除 rag:col: 下所有 key（WriteBatch 批量 del）
```

### 7.2 文档入库

```
POST /api/rag/collections/:col/documents
  Content-Type: multipart/form-data (文件) 或 application/json (文本)
  Response: { task_id, doc_id, estimated_chunks }

GET  /api/rag/tasks/:task_id
  Response: { status:"pending|running|success|failed", progress, error }

GET  /api/rag/collections/:col/documents
  Response: { items:[DocumentMeta], total }  — 底层走 Iterator.seek 前缀扫描

GET  /api/rag/collections/:col/documents/:doc_id
  Response: DocumentMeta + { chunks:[ChunkRecord] }

DELETE /api/rag/collections/:col/documents/:doc_id
  — WriteBatch 删除该文档所有 chunk key + meta key + status key，同步更新 side index
```

### 7.3 检索

```
POST /api/rag/collections/:col/retrieve
  Body: { query, top_k, filters:{}, debug:bool }
  Response: {
    hits: [{ chunk_id, score, text, doc_id, heading, page_num }],
    rewrite_query,  // debug 时返回
    latency_ms
  }
```

### 7.4 流式问答

```
POST /api/rag/collections/:col/chat
  Body: { query, session_id, top_k, history_turns:3 }
  SSE 事件流:

  event: token
  data: {"text":"当"}

  event: token
  data: {"text":"前"}

  event: citation
  data: {"chunk_id":"...", "doc_id":"...", "title":"...", "score":0.87}

  event: debug
  data: {"rewrite_query":"...", "top_k":5, "prompt_tokens":820}

  event: end
  data: {"total_tokens":1120, "latency_ms":1340}

  event: error
  data: {"code":"provider_error", "msg":"..."}
```

---

## 8. 核心流程设计

### 8.1 文档入库流程

```
用户上传文件
      │
      ▼
RAG service 创建 IngestTask（写 rag:task:{task_id}）
      │
      ▼ 异步 goroutine
解析文件 → 纯文本（txt/md 直接读，pdf 调外部命令）
      │
      ▼
文本清洗（去多余空行、统一 UTF-8、去页眉页脚噪声）
      │
      ▼
去重检查（sha256 → 查 rag:doc:{col}:*:meta 是否有相同 content_hash）
如已存在 → 返回 doc_id，跳过后续步骤
      │
      ▼
Chunker（固定窗口 + 标题边界感知）
      │
      ▼
批量 Embedding（每批 32 个 chunk，失败分片重试）
      │
      ▼
WriteBatch 原子入库：
  所有 chunk key → minikv
  doc meta key  → minikv
  doc status    → "success"
  col stats     → 更新
      │
      ▼
更新 Side Index（chunk_id → vector）
      │
      ▼
写快照 rag:index:{col}:snap:{ver} → 快照文件路径
      │
      ▼
更新任务状态 rag:task:{task_id} → success
```

**关键容错点**
- WriteBatch 写完之前，task status 保持 "running"，对外不可见。
- WriteBatch 失败 → task status 改 "failed"，下次重新提交，重用 doc_id。
- Side index 写失败 → doc status 保持 "success"（文本已持久化），重启后可用 Scan 重建。

### 8.2 检索流程（热路径）

```
query 字符串
      │
      ▼
（可选）query rewrite — 扩展同义词或重写
      │
      ▼
embed(query) → float[]
（先查 rag:cache:emb:{sha256(query)}，命中则跳过远端调用）
      │
      ▼
side index.TopK(vec, k, filters)
  → [(chunk_id, score), ...]
      │
      ▼
并发 db.Get(chunkKey(col, docID, seq)) × top_k
（BloomFilter 过滤无效 key，LRU cache 命中热 chunk）
      │
      ▼
（可选）rerank（第一阶段为 stub，直接返回）
      │
      ▼
写 rag:qlog:{col}:{req_id}（异步，不阻塞响应）
      │
      ▼
返回 RetrievalHit 列表
```

**为什么并发 Get 是可行的**：minikv 的 `get()` 走 MemTable 读锁 + SSTable 只读，多个并发 Get 不争写锁，且 BloomFilter 让绝大多数"不存在 key"的 SSTable 查询立即返回。

### 8.3 流式问答流程

```
retrieve(query, top_k)
      │
      ▼
按 score 排序，取 top_k chunks
      │
      ▼
回查会话历史（Iterator.seek("rag:chat:{uid}:{sid}:") 取最近 N 条）
      │
      ▼
assemble prompt：
  system: "根据提供的上下文回答，不要编造，无法回答时说明。"
  context: chunk[0..k-1]（每条带 doc_id/title/score）
  history: [user, assistant, ...]（最近 history_turns 轮）
  user: query
      │
      ▼
chat provider.StreamComplete(prompt)
      │ token by token
      ▼
SSE 推送 event:token / event:citation / event:end
      │
      ▼
写会话历史 WriteBatch：
  rag:chat:{uid}:{sid}:{seq:08d} → user message
  rag:chat:{uid}:{sid}:{seq+1}   → assistant message + citations
```

---

## 9. minikv Options 参数调优建议

RAG 场景下，建议 `services/rag/` 的 minikv 连接使用专用 db_path（或 namespace prefix 共享同一实例），并根据 RAG 特点调整参数：

```go
// RAG 优化后的 Options
opts := minikv.Options{
    DBPath:                  "/data/titankv/rag",
    MemtableSize:            16 * 1024 * 1024,   // 16MB：chunk 写入批量大，加大 memtable
    BlockSize:               8 * 1024,            // 8KB：chunk 文本通常 500–2000 字，适当加大 block
    LRUCacheCapacity:        64 * 1024 * 1024,   // 64MB：热 chunk 缓存，retrieval 热路径受益
    BloomFilterEnabled:      true,
    BloomFalsePositiveRate:  0.01,                // 默认，可调低到 0.005 提升 Get 性能
    Compression:             2,                   // zstd：文本压缩率比 snappy 高 ~20%，chunk 场景推荐
    MaxLevel:                7,                   // 默认，RAG 数据量不超 100GB 时不需要改
    Level0CompactionTrigger: 4,
}
```

**参数选择理由**

| 参数 | 调整值 | 理由 |
|---|---|---|
| MemtableSize | 16MB | 文档批量入库时写入量大，大 MemTable 减少 flush 频率 |
| LRUCacheCapacity | 64MB | 热知识库高频被检索，chunk 缓存命中率可达 80%+ |
| Compression | zstd(2) | 中文文本 zstd 压缩率约 3–5x，比 snappy 高 1–2x |
| BlockSize | 8KB | chunk 正文平均 1–3KB，8KB block 能放 2–4 个 chunk，减少 IO |

---

## 10. 向量 Side Index 设计

### 10.1 第一阶段（MVP）：内存 + 文件快照

```go
type SideIndex struct {
    mu      sync.RWMutex
    vectors map[string][]float32  // chunk_id → float32 vector
    dim     int
    metric  string                // "cosine"|"dot"|"l2"
}

func (idx *SideIndex) TopK(query []float32, k int) []Hit { ... }
func (idx *SideIndex) Add(chunkID string, vec []float32)  { ... }
func (idx *SideIndex) Delete(chunkID string)               { ... }
func (idx *SideIndex) SaveSnapshot(path string) error      { ... }  // 序列化到文件
func (idx *SideIndex) LoadSnapshot(path string) error      { ... }
```

快照持久化：
- 每次文档入库成功后，写快照文件到 `RAG_INDEX_DIR/{col}/{ver}.idx`。
- 快照路径写入 `rag:index:{col}:snap:{ver}`，启动时通过 Iterator.seek 找最新版本。

```go
// 重建流程（服务重启）
iter.Seek(fmt.Sprintf("rag:index:%s:snap:", col))
latestSnap := ""
for ; iter.Valid() && strings.HasPrefix(iter.Key(), ...) ; iter.Next() {
    latestSnap = string(iter.Value())  // 最后一条 = 最新快照
}
idx.LoadSnapshot(latestSnap)
// 若快照版本落后，增量重建：扫描 rag:chunk:{col}: 前缀补齐缺失向量
```

### 10.2 第二阶段：引入 HNSW 库

待 MVP 稳定后，替换 `SideIndex` 实现为 [hnswlib-go](https://github.com/hnswlib/hnswlib) 或 C++ HNSW（cgo 封装）。接口不变，上层 `retrieve.go` 无需修改。

### 10.3 与 minikv 的关系

- minikv 不存向量（float32 数组压缩率低，不划算）。
- minikv 存 chunk text / chunk meta / index version / snapshot path。
- side index 存 chunk_id → float32。
- 两者通过 chunk_id 关联，删除文档时 WriteBatch 删 minikv，同时 `idx.Delete(chunkID)`。

---

## 11. Chunker 策略

### V1：固定滑动窗口（第一阶段默认）

```go
func FixedWindowChunker(text string, size, overlap int) []Chunk {
    // 按 token 数切分（估算：4字节≈1token，中文约2字≈1token）
    // 每个 chunk [charStart, charEnd]
    // overlap 保证上下文连续性
}
```

参数：`chunk_size=512 tokens`，`chunk_overlap=64 tokens`。

### V1.5：标题感知（Markdown 优先）

对于 Markdown/设计文档，按 `##` / `###` 边界优先切割，每段不超过 size 上限，超出则再滑动。好处：chunk 的 heading 字段可以准确标注，retrieval 结果更好解释。

### V2：文档类型特化（后续）

- PDF：按页码 + 自然段切割，保留 page_num。
- 代码文档：按函数/类注释块切割。
- FAQ：按问答对切割。

---

## 12. 评测体系

### 12.1 离线评测接口

```
POST /api/rag/collections/:col/eval
Body: {
  cases: [
    { query:"...", expected_doc_ids:["docA","docB"] },
    ...
  ],
  top_k: 5
}
Response: {
  recall_at_k: 0.82,
  mrr: 0.74,
  avg_latency_ms: 23,
  cases: [{ query, hits, matched_expected, score }]
}
```

底层：对每个 case 跑 retrieve，对比 hits 的 doc_id 是否命中 expected。

### 12.2 Prometheus 指标

```
rag_ingest_tasks_total{col,status}
rag_ingest_chunk_count_total{col}
rag_embedding_latency_ms{col,provider}
rag_retrieve_latency_ms{col}
rag_retrieve_empty_total{col}
rag_chat_latency_ms{col}
rag_chat_tokens_total{col,type}   // type=prompt|completion
rag_cache_hit_total{type}         // type=embedding|chunk
```

---

## 13. 配置项

```bash
# services/rag 专用环境变量
RAG_EMBEDDING_PROVIDER=openai        # openai|custom_http|local_python_sidecar
RAG_EMBEDDING_MODEL=text-embedding-3-small
RAG_EMBEDDING_DIM=1536
RAG_EMBEDDING_BATCH_SIZE=32
RAG_EMBEDDING_BASE_URL=https://api.openai.com/v1
RAG_EMBEDDING_API_KEY=sk-...

RAG_CHAT_PROVIDER=openai
RAG_CHAT_MODEL=gpt-4o-mini
RAG_CHAT_BASE_URL=https://api.openai.com/v1
RAG_CHAT_API_KEY=sk-...

RAG_INDEX_DIR=/data/titankv/rag-index
RAG_MAX_DOC_SIZE_MB=20
RAG_DEFAULT_TOPK=5
RAG_ENABLE_RERANK=false

MINIKV_ADDR=127.0.0.1:9000   # 复用现有配置

RAG_SERVICE_PORT=8085
```

---

## 14. 分阶段实施计划

### Phase A — 骨架（1–2 天）

目标：服务启动，路由可达，与 minikv 连接验证。

任务：
1. 新建 `services/rag/` 目录结构。
2. `config.go` 读所有环境变量。
3. `store.go` 封装 minikv TCP 客户端，验证 Get/Put/Scan 前缀扫描可用。
4. `router.go` 注册占位路由，返回 `{"status":"ok"}`。
5. `gateway/` 新增 `/api/rag/` 代理路由。
6. `cmd/main.go` 集成到 `make run-all`（新增 `:8085`）。

验收：
- `go build ./...` 通过。
- `GET /api/rag/healthz` 返回 200。
- `store_test.go` 的前缀 Scan 单元测试通过。

---

### Phase B — 文档入库 MVP（3–4 天）

目标：上传文本/Markdown 文档，任务成功，chunk 可列出。

任务：
1. `chunker.go` V1 实现，单元测试覆盖边界。
2. `provider_embedding.go` HTTP 接口，支持批量嵌入 + 重试。
3. `ingest.go` 入库编排（异步 goroutine + task_store）。
4. `task_store.go` 读写 `rag:task:` 前缀。
5. `POST /api/rag/collections/:col/documents` + `GET /api/rag/tasks/:task_id`。
6. 入库成功后初始化 `vector_index.go` side index 并写快照。

验收：
- 上传 TitanKV README，task 状态变 success。
- `GET /api/rag/collections/:col/documents` 返回文档。
- Iterator.seek 前缀扫描能列出所有 chunk。
- 重复上传同一文档（相同 content_hash）不污染索引。

---

### Phase C — 检索 MVP（2–3 天）

目标：query 能召回相关 chunk，debug 能看 score。

任务：
1. `retrieve.go` 流程：embed → side index TopK → 并发 db.Get chunk text。
2. `POST /api/rag/collections/:col/retrieve`，支持 `debug=true` 返回分数。
3. query embedding 缓存（`rag:cache:emb:{hash}` 写 minikv，TTL 通过 expiry 字段自行判断）。
4. 异步写 query log（`rag:qlog:` 前缀）。

验收：
- 查"minikv 写路径是什么"能召回 minikv 设计文档相关 chunk。
- debug=true 时返回 score 与命中 chunk 摘要。
- 并发 10 个 retrieve 请求下，响应 p99 < 100ms（不含 embedding 远端调用）。

---

### Phase D — 流式问答（2–3 天）

目标：前端能看到流式回答，能看到来源引用。

任务：
1. `chat.go` 编排：retrieve → 历史回查 → prompt assemble → LLM SSE。
2. `provider_chat.go` 实现 ChatProvider，支持 SSE token 流。
3. `prompt.go` 三段式组装，debug 模式下返回完整 prompt。
4. `POST /api/rag/collections/:col/chat` SSE 接口。
5. 会话历史用 WriteBatch 原子写到 `rag:chat:` 前缀。
6. Web 聊天页接入，`ChatWindow.tsx` 消费 SSE，`CitationPanel.tsx` 展示来源。

验收：
- 页面能流式显示回答。
- 能看到引用的文档标题和 chunk 摘要。
- 会话刷新后，历史可通过 Iterator.seek 回放展示。

---

### Phase E — 加固与收口（3–5 天）

目标：从"能跑"升级到"能演示、能答辩"。

任务：
1. 文档删除联动删除 side index 与 minikv 所有 chunk key（WriteBatch 批量 del）。
2. Embedding/LLM provider 错误码分层（超时 / provider_error / invalid_response）。
3. Prometheus 指标接入 `services/observability/`。
4. 评测接口 `/api/rag/eval`，跑 3 份 demo 文档基线。
5. 知识库权限收口（`gateway/` RBAC 扩展 rag resource）。
6. `RUN.md` 补充 RAG 启动说明与环境变量。

验收：
- 10 个基线问题召回率 ≥ 70%。
- Prometheus 能看到 `rag_retrieve_latency_ms`。
- `make run-all` 一键起所有服务含 RAG。

---

## 15. 测试计划

### 15.1 单元测试

| 测试文件 | 覆盖点 |
|---|---|
| `store_test.go` | key 编解码、前缀 Scan、WriteBatch 原子性（模拟崩溃） |
| `chunker_test.go` | 固定窗口边界、overlap 正确、空文档、超长文档 |
| `vector_index_test.go` | cosine/dot 相似度结果、TopK 排序、Add/Delete 正确性 |
| `provider_test.go` | embedding mock 批量、超时重试、HTTP 错误码处理 |
| `ingest_test.go` | 重复入库去重、任务状态流转、WriteBatch 失败回滚 |

### 15.2 集成测试

1. 上传 → task success → 文档/chunk 可查询
2. retrieve → topK 有序返回，score 合理
3. chat SSE → token/citation/end 事件完整
4. 删除文档 → retrieve 不命中已删除 chunk

### 15.3 端到端 Demo 文档集

三份内置测试文档：
- `minikv/README.md`
- `skynet/docs/design.md`
- `minikv/docs/design.md`

基线问题：
1. TitanKV 的四层架构是什么？
2. minikv 的写路径经过哪些组件？
3. skynet 使用了哪种 IO 模型？
4. WAL 崩溃恢复是怎么做的？
5. 什么情况下会触发 Compaction？

要求：≥ 3/5 问题引用到正确文档，无法回答时显式说"文档中未找到"。

---

## 16. 风险与修复策略

| 风险 | 影响 | 应对 |
|---|---|---|
| side index 与 minikv 双写不一致 | chunk text 有、vector 无，导致召回后 Get 失败 | 先写 minikv（WriteBatch），再写 side index；重启时 Scan 前缀比对，补齐缺失 vector |
| embedding provider 不稳定 | 入库卡住，用户体验差 | 批量失败支持分片重试；provider 超时单独计数；task status 实时更新 |
| 文档过大导致 embedding 请求超时 | 入库失败 | 异步任务化；按 batch 处理，失败只重试该 batch |
| chunk 召回质量差 | 问答答非所问 | 先用 debug retrieve 页面调参（chunk_size/top_k）；预留 rerank 接口 |
| SSE 断流 | 前端只显示半截回答 | 前端监听 error 事件，显示"回答中断，可重试"；服务端写 partial answer log |
| minikv 磁盘写满 | 入库失败 | 日志打印剩余空间；ingest 前预检；可配置 max_doc_size_mb |

---

## 17. 简历表述建议

主条目：

> 在 TitanKV 上扩展 RAG 知识库能力：以自研 C++ LSM 引擎（minikv）持久化 chunk 文本与元数据，利用 Iterator seek 前缀扫描做文档/chunk 遍历，WriteBatch 保障文档入库的原子一致性，BloomFilter + LRU Cache 加速 retrieval 热路径；新增 Go RAG 编排服务完成文档切块、Embedding、向量检索与流式问答，通过 C++20 协程网关（skynet）对外提供 SSE 输出。

追问预案：
- "为什么不用现成向量数据库（如 Milvus/Qdrant）？" → 第一阶段用 side index，后续可插拔替换；minikv 做底座的价值在于存储故事的完整性和可控性。
- "chunk 入库如何保证一致性？" → WriteBatch 原子写，WAL 保障宕机恢复，状态 key 三值流转。
- "检索性能如何？" → BloomFilter 跳过无效 SSTable，LRU Cache 命中热 chunk，并发 Get 不争写锁，p99 < 100ms（不含 embedding）。
- "向量和文本为什么分开存？" → 文本 snappy/zstd 压缩率高（3–5x），float32 向量压缩效果差，分开存降低存储成本；也避免 minikv 核心文件格式改动。

---

## 18. 不做什么（明确边界）

第一阶段明确不做以下内容，避免范围蔓延：

1. HNSW / IVF-PQ 内核自研（side index 够用，后续可换库）
2. 多租户计费（auth 层已有 APIKey 基础，计费另立需求）
3. PDF 全文 OCR（外部命令 sidecar 足够 MVP）
4. 多模型智能路由（单 provider 先跑通）
5. 分布式向量分片（rag 数据量 < 100GB 不需要）
6. 把向量塞进 minikv SSTable（会破坏存储引擎主线叙事，MVP 用 side index）

---

## 19. 里程碑汇总

| 里程碑 | 标志 | 预计工期 |
|---|---|---|
| M1 骨架上线 | `/api/rag/healthz` + Scan 单测通过 | 第 1–2 天 |
| M2 文档入库 | 上传 README，chunk 可列出 | 第 3–6 天 |
| M3 检索可用 | query 召回相关 chunk，有 score | 第 7–9 天 |
| M4 流式问答 | 前端流式显示回答 + 来源引用 | 第 10–12 天 |
| M5 可演示 | 3 份文档，5 个问题 ≥ 3 命中，指标可见 | 第 13–17 天 |

---

*文档结束 — 版本 v2.1。本文为设计依据；实现以 `services/rag/` 与 `docs/RAG-ARCHITECTURE.md` / `docs/TITANWIKI-ARCHITECTURE.md` 为准。*
