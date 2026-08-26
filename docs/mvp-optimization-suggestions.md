# TitanKV / SpectrumCore — MVP 级模块优化建议

> 版本：v1.2 · 日期：2026-08-26 · 分支基线：`feat/industrialization`
>
> **进度**：M1–M4✅ M5✅ M6✅ M7(最小)✅ M8(部分)✅ M9✅ M10✅ T2.1✅ T2.3✅ T2.5✅ T2.6(最小)✅ · 下一步 **RAG 质量 / Web·CLI**
>
> 范围：仅覆盖现状报告中标为 **MVP** 的模块；不含已标「较完善」的 P0 正确性地基，也不把 Raft/K8s 等**教学级**当成可「升生产」的对象。
>
> 诚实边界（全文遵守）：
> - 建议对齐 `docs/industrialization-plan.md` 的 M5–M10 / T2.x，不发明计划外能力。
> - 「优化」= 从 MVP 抬到「面试可讲清 + 有回归测试的正确性/有界资源」；**不等于** RocksDB / 生产多机集群。
> - 每条建议带：痛点证据 → 建议动作 → 验收 → 对应计划编号 → 预估优先级。

---

## 0. MVP 清单与优先级总览

| # | MVP 模块 | 最大风险 | 建议优先做 | 计划锚点 |
|---|----------|----------|------------|----------|
| 1 | minikv 读校验 / Bloom | 损坏静默降级；磁盘泄漏 | **P0 下一刀** | M5、M6 |
| 2 | minikv 网络边界 | 内存 DoS；写阻塞卡死 | P0 | M8 |
| 3 | RAG 快照 / 删除一致性 | 重启静默缺检索；幽灵向量 | P0 | M7、M10 |
| 4 | MemTable / 压缩内存 | 大表 O(n) 读；压缩爆内存 | P1 | M9、T2.1、T2.3、T2.5 |
| 5 | skynet_gateway | 已有部分上限，缺统一运维面 | P1 | M8 旁路对齐 |
| 6 | Gin + Auth / Data / Observ | 可跑但缺引擎指标与压测证据 | P1–P2 | T2.6、T2.8 |
| 7 | RAG 检索质量 | chunker/HNSW 教学级 | P2 | T2.9、T2.10 |
| 8 | Web / client-cli | 演示够用，SDK/体验弱 | P2 | Phase 8 |

**推荐落地顺序（诚实、可面试）：**  `M5 → M6 → M7+M10 → M8 → M9 → T2.3/T2.1/T2.5 → T2.6 → RAG 质量 → Web/CLI`。

---

## 1. minikv — SST 读取与 Bloom（仍属引擎 MVP 缺口）

### 1.1 现状（已核实）

- `SSTableReader::lookup` 在 `readBlock` 失败时仍返回 `kMiss`（注释写明「M5 will promote to Corruption」）→ 损坏 ≈ 找不到。
- `BloomFilter(10000, 0.01)` 硬编码于 `sstable_builder.cpp`。
- `purgeOrphanSSTables` 只清 `.sst`，compaction/`unlink` 源 SST 时未连带 `.bloom`。

### 1.2 优化建议

| 项 | 建议 | 验收 | 计划 |
|----|------|------|------|
| **显式错误传播** | `readBlock` 校验 offset/size/上限；index CRC 必验；`lookup` 对 Corruption/IOError 上抛，`DBImpl::get` 返回 IOError（Go Data → 500） | `test_sstable_corruption.cpp`：翻转字节 / 截断 / 伪造巨大长度 → Corruption，不 OOM | **M5** |
| **Bloom 生命周期** | 孤儿 `.bloom` 纳入 purge；删 SST 时 `unlink(path+".bloom")`；`finish` 按真实 `entry_count` 建 filter | 孤儿场景测试；10 万 key FP ≤ 配置×1.2 | **M6** |
| **格式（可后置）** | Bloom v2 magic/version/CRC（T2.4）——先做条数与生命周期，再格式升级 | 损坏 bloom 不可静默当「无 filter」 | T2.4 |

**面试口述：** 「现在能跑，但坏盘会静默丢数据；下一步先把 Corruption 和 NotFound 拆开。」

---

## 2. minikv — 网络协议边界（M8）

### 2.1 现状（已核实）

- `connection.cpp` 持续 `read_buf_.append`，**无** `kMaxRequestSize`。
- 写路径注释「caller should wait for EPOLLOUT」，但缺完整 EPOLLOUT 注册/注销与写缓冲上限。
- 对比：`skynet/gateway/main.cpp` 已有 `kMaxRequestBytes = 1<<20` —— **skynet 比 minikv_server 更严**，不对称。

### 2.2 优化建议

1. **请求上限**：默认 64MB（Options 可配），超限断连。
2. **EPOLLOUT**：EAGAIN 注册、写空注销；写缓冲上限（如 256MB）超限断连。
3. **idle timeout**：如 60s 无完整请求断开（防 slowloris）。
4. **可选 `--auth-token`**：默认关；文档写清「内网可关、公网必须开」。
5. **验收**：fuzz 30s 无崩溃/FD 泄漏；慢客户端断开；100MB value → 显式错误。

**诚实边界：** 这是单机引擎端口加固，不是 TLS/mTLS 网关替代品（鉴权主战场仍在 Gin）。

---

## 3. minikv — 读放大与内存有界（MVP→性能地基）

| 痛点 | 建议 | 验收 | 计划 |
|------|------|------|------|
| `MemTable::lookup` 仍 `entries()` 全表拷贝后扫 | `SkipList::lowerBound` + 惰性 iterator | bench + deletion/多版本正确性 | **M9** |
| 每次 Get `SSTableReader::open` 新 fd | `TableCache`（LRU，默认 128）统一 get/iter/compaction；删文件 Evict | `/proc/self/fd` 随 QPS 近似恒定 | **T2.3** |
| compaction 全量物化 `vector` | 流式 k-way `MergingIterator` → `.tmp` → rename → manifest | 1GB 合并峰值 RSS 对比入库 | **T2.1** |
| immutable / L0 无背压 | `kMaxImmutable`、`level0_stop_writes_trigger` → Busy/阻塞可配 | 60s 写压 RSS 有界、L0≤阈值 | **T2.5** |
| Scan/DeleteRange 全量物化 | 协议 `limit` + `has_more`；DeleteRange 分批 | 大表扫描服务端内存有界 | **T2.8** |

**优先说明：** M9/T2.3 性价比高（改动面相对小、面试故事清晰）；T2.1/T2.5 工作量大，放在正确性 M5–M8 之后。

---

## 4. RAG 服务（MVP）

### 4.1 崩溃一致性与删除（P0）

**现状：**

- `SaveSnapshot` / `SaveSnapshotPrefix` 直接 `os.Create` + **JSON** 编码，无 tmp→fsync→rename→dir fsync。
- `DeleteDocument`：先 `store.DeleteDocument`，再 `index.ClearByPrefix`，**不重写快照** → 重启可能从旧快照「复活」向量。
- 计划承诺的 `RebuildFromStore`（按 `rag:chunk:` 前缀重建）**未实现**。

**建议（按序）：**

1. **M7** 原子快照 + 二进制格式 v2（magic/CRC）+ 启动校验失败则重建。
2. **M10** 封装 `CollectionService.Delete`：KV 批删成功 → `ClearByPrefix` → 快照原子重写；失败显式错误、幂等可重试。
3. e2e：`ingest → kill → 重启 → retrieve`；`delete → 重启 → 检索空`；删快照文件 → 自动重建。

### 4.2 检索质量（P2，勿与正确性抢档期）

| 项 | 建议 | 计划 |
|----|------|------|
| Chunker | 软边界句子切分；UTF-8 token 估算；`#{1,6}` 层级路径；`chunker_version` 强制重嵌 | **T2.9** |
| 向量索引 | Add 时预归一化；HNSW ef/M、mark-delete、图持久化；小 N 回退 brute | **T2.10** |
| 缓存 | emb/chat TTL + 容量上限 | T2.10 |

**诚实边界：** 召回提升必须带 eval 前后数据（已有 `eval_test`）；禁止只改算法不落数字。

---

## 5. skynet_gateway（MVP）

### 5.1 现状亮点（勿重复造轮）

- 已有请求体上限 `kMaxRequestBytes`、协程 + EPOLLOUT awaitable、连接池 idle。

### 5.2 建议

1. **与 minikv M8 对齐文档**：公开入口 :8080 的极限（最大 body、超时、上游 Gin 超时）写进 `skynet/docs/design.md` / `RUN.md`。
2. **可观测**：连接数、上游失败率、协程调度延迟 — 先打日志/简单计数，再接到 observability（勿声称已有 Jaeger 全链路）。
3. **故障注入**：上游 Gin 挂掉 / 半包 / 慢读 的集成测试（现有 reactor 单测不够覆盖代理路径）。
4. **不做**：JWT/限流（故意在 Gin；面试讲清分层即可）。

---

## 6. Gin gateway + Auth（MVP）

### 6.1 已有

中间件链：RequestID / RateLimit / Auth / RBAC / Recover / Logger；代理注入 `X-Request-ID` + `X-User-ID` + `X-Role`。

### 6.2 建议

| 方向 | 建议 | 诚实边界 |
|------|------|----------|
| **配置** | RateLimit / JWT TTL / 上游超时全部环境变量化并启动打印 | 避免「改代码才能调参」 |
| **错误语义** | 上游 5xx / 超时 / minikv IOError 映射表文档化 | 与 M5 联动 |
| **测试** | 中间件顺序与「health/login 跳过 Auth」的表驱动测试 | 防回归 |
| **安全** | APIKey 轮换、审计日志（谁删了哪 key） | 仍是教学网关，不做 WAF |
| **分片路由** | `shardRouter` 若仅定义未挂载，文档标「辅助/未接线」 | 防虚构分布式 |

---

## 7. Data / Meta / Observability（MVP）

### 7.1 Data

- **现状：** `MINIKV_ADDR` → TCP MiniKVClient；无地址则内存 `Store`（已在代码注释标 MVP fallback）。
- **建议：**
  1. 启动时 **强制日志** `backend=minikv|memory`，控制台/CLI 可见，避免面试演示误用内存。
  2. Scan 分页（等 T2.8 协议）后改 `ScanPage`，禁止服务端无界物化。
  3. 客户端超时、重试、错误码与 M5 IOError 对齐。
  4. **不做：** 本阶段硬上 gRPC/cgo（README 仍 planned）。

### 7.2 Meta

- 保持薄：用户/集合元数据 CRUD 即可。
- 建议：与 RAG collection 生命周期对齐的级联删除策略（文档写清「meta 删了 RAG 是否级联」）。

### 7.3 Observability

- **痛点：** 刮 Go 层为主；引擎零 `/metrics`（计划 T2.6）。
- **建议：**
  1. minikv `GET /metrics` + `/healthz`（`titankv_engine_*`）。
  2. scraper 增加 minikv job；仪表盘 JSON 入库 `docs/`。
  3. README 删除或降级「已有完整 Jaeger」类不可兑现句（防虚构风险）。

---

## 8. Web 控制台（MVP）

### 8.1 现状

`web/app/dashboard/`：data / rag / cluster / users / collections — 演示面够用。

### 8.2 建议

1. **后端真相优先：** Cluster 页明确标注 Raft 为教学实验；禁止 UI 暗示多机生产就绪。
2. **错误可见：** Data backend=memory 时仪表盘黄条警告。
3. **RAG：** ingest 任务状态、快照健康（count vs chunk 数）——依赖 M7 暴露只读 API。
4. **工程：** 补最小组件测试或 Playwright 烟雾（登录→put→get）；现 Makefile 仍写「无 unit test script」。
5. **YAGNI：** 不上复杂设计系统；保持与 API 一一对应。

---

## 9. client-cli（MVP）

### 9.1 现状

Cobra：`ping` / `put` / `get` / `scan` 等基础 KV；多语言 SDK 仍 planned。

### 9.2 建议

1. 补 `delete`、分页 `scan --limit`、`--json` 输出（脚本友好）。
2. 统一 `--addr` 指向 **公开入口 :8080（skynet）** 或 Gin，文档写死推荐拓扑。
3. 退出码约定（NotFound=2、IOError=3），方便 CI。
4. **不做：** 完整多语言 SDK；面试说「CLI MVP，SDK 按需」即可。

---

## 10. 明确不在本清单「优化成生产」的对象

| 模块 | 档位 | 建议 |
|------|------|------|
| Raft / 分片迁移 | 教学 | 保持 `docs/demo-distributed.md` 级演示；措辞「最终一致性双写窗口」 |
| K8s / Helm / CI 全家桶 | planned | 有余力再做最小 compose；勿先写「已生产部署」 |
| gRPC 包裹 minikv | planned | TCP MVP 足够支撑校招叙事 |

---

## 11. 执行纪律（复用工业化计划 §1）

1. **红灯测试先行**（复现失败 → 修复 → 绿）。
2. **每项独立 commit**：`fix:` / `feat:` + `[M5]` 等编号。
3. **阶段门禁**：`make cpp-test` + `make go-test`；涉及引擎时再跑 ASAN 聚焦用例。
4. **诚实更新**：做完一条就改本文件状态与 `industrialization-plan.md` 对应勾选，不写预期外结论。

---

## 12. 一页纸「下一步就动手」清单

若只开下一个 PR，建议最小切片：

1. **[M5]** `test_sstable_corruption` 红 → `readBlock` 边界 + lookup 错误传播 → 绿  
2. **[M6]** 孤儿 `.bloom` purge + 删 SST 连带 unlink  
3. **[M7 最小]** 快照 tmp+rename+fsync（可先保留 JSON，二进制 v2 第二 PR）  
4. **[M10]** DeleteDocument 后重写快照 + e2e  

以上四项全部是「正确性」，符合计划「阶段一不做性能」的纪律；性能项（M9/T2.*）排在其后。

---

## 参考

- `docs/industrialization-plan.md`（v1.2，M1–M4 已达标）
- `docs/industrialization-status-2026-08-26.html`（三分法现状页）
- `docs/ERRORLOG.md`（E1 Get 三态教训：正确性改动要成对）
- 代码锚点：`minikv/src/core/sstable_reader.cpp`、`sstable_builder.cpp`、`services/rag/{handler,vector_index,hnsw_index}.go`、`minikv/src/network/connection.cpp`、`skynet/gateway/main.cpp`


## Changelog

- **v1.2 (2026-08-26)**: M9✅ T2.3✅ T2.1✅ T2.5✅ T2.6(最小)✅；ctest 待本轮全量确认。
