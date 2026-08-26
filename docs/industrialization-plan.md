# TitanKV 工业化改造计划（从教学级 → 工业级）

> 版本：v1.1 · 日期：2026-08-26 · 状态：阶段一 P0（M1–M4）已落地；M5+待续
>
> 目标：把 SpectrumCore（TitanKV）从一个"能跑、有测试、但只覆盖教学知识点"的项目，
> 提升为一个**数据安全、边界健壮、性能有据、可观测、可演示**的真实可用的分布式 KV + RAG 平台，
> 让面试官追问任何一层都能给出"为什么这么设计"的工程答案。
>
> 诚实边界（全文遵守）：
> - 每一节标注 [已达标] / [教学级] / [虚构风险]，不把计划当现状，不把 demo 当生产。
> - 任何"修复"必须有复现测试先行（红 → 绿），禁止靠打补丁蒙混。
> - 出错不修补：回滚该模块重做，根因写入 `docs/ERRORLOG.md`。

---

## 0. 现状基线（2025-08-25 实测）

| 项目 | 实测结果 |
|---|---|
| C++ 单元测试 | 77/77 通过（3.15s），含 WAL/Manifest 重开、压缩回退、LRU、skiplist |
| Go 测试 | 全部通过（distributed/data/rag cached ok） |
| 构建 | `ninja: no work to do`（增量干净），ctest 全绿 |
| 未提交改动 | `docs/layers/*` 7 个文件 +1329 行（文档层知识图谱，未提交） |
| 架构 | C++17/20 引擎(minikv) + C++20 协程网关(skynet) + Go 微服务 + Next.js 控制台 |

### 0.1 已达标（诚实清单，面试可放心讲）

- WAL 组提交：leader/follower 组队、合并单次 append + 单次 fdatasync、seq 顺序与 WAL 字节序一致 —— 设计正确。
- 多 WAL 文件 + 异步 flush：写路径只做"冻结 memtable + 轮转 WAL + 入队"，重 IO 在后台线程，方向正确。
- MVCC InternalKey：user_key 升序 + seq 降序 + 类型位，块内/跨文件排序逻辑正确。
- 块级 CRC + snappy/zstd 压缩回退（压缩后更大则存原样）—— 正确。
- 孤儿 SST 清理（purgeOrphanSSTables）思路正确。
- Go 层微服务拆分、JWT/RBAC/限流中间件链、RAG ingest→retrieve→chat 全流程可跑。
- 77 个单测 + eval_test（Recall@K/MRR 函数级）已有，是进一步工作的地基。

### 0.2 已核实的关键缺陷（按严重度排序，详见各阶段）

1. **P0 正确性**：compaction 丢弃非最底层 tombstone → 删除数据复活（compaction.cpp:119）。
2. **P0 数据丢失**：WAL replay 不截断损坏尾部 → 后续写入永久不可恢复（wal.cpp:38-52）。
3. **P0 数据丢失**：MANIFEST replay 同样不截断；层数硬编码 8/7 与 max_level 脱钩（manifest.cpp / version.cpp）。
4. **P0 静默降级**：SST 读取错误被当 NotFound 吞掉（sstable_reader.cpp get）；index block CRC 写了不校验；readBlock 无边界/上限校验。
5. **P1 内存爆炸**：compaction 全量物化所有源文件到内存；MemTable::get 每次全表拷贝 O(n)；Scan/DeleteRange 服务端全量物化。
6. **P1 崩溃一致性**：RAG 向量索引只存内存，快照是 JSON 非原子写、无校验；文档声称"重启从 minikv 重建"但代码未实现 → 重启后检索静默失效。
7. **P1 资源泄漏**：`.bloom` 孤儿文件永不清理；SST 每次访问重新 open fd 无句柄缓存。
8. **P1 网络健壮性**：无请求大小上限、无 EPOLLOUT 重注册（写阻塞即卡死）、无 idle timeout、无鉴权。
9. **P2 设计缺陷**：Bloom 硬编码 10000 keys；memtable 无限堆积（无 write stall）；可观测性只覆盖 Go 层，引擎零指标。
10. **P2 检索质量**：chunker 无句子边界/无 token 估算精度；向量检索暴力扫描未归一化预计算；HNSW 简化版无持久化、Delete 留悬垂边。



### 0.3 本轮实际达成（2026-08-26）

| 条目 | 实际达成 | 未达成 / 诚实边界 |
|---|---|---|
| M1 tombstone | 仅最底层 drop；Get 三态（miss/value/tombstone）；同层新文件优先扫描；`waitCompaction` | 未做范围感知删除（T2.2）；L0 compact 仍不 merge 目标层重叠文件 |
| M2 WAL torn tail | `replay` `ftruncate` 到 good_offset + WARN 日志；测试覆盖截断后 append | 未做 kill-9 注入（F1） |
| M3 MANIFEST | 同 M2 截断；`level_count=max_level+1` 传入 Manifest/Version；配置偏小时保留盘上层跨度 | kReset+level_count 仅在 `recordReset` 路径写入；rewriteSnapshot 仍为纯 kAdd 快照 |
| M4 dir fsync | `utils::fsyncDir`；rewriteSnapshot rename 后、openWal/rotateWal 后调用 | WSL 无法真实模拟掉电；以 fsync 顺序+进程杀为准 |

验收：`ctest` 全绿（含新增 CompactionTombstone / WAL torn / Manifest truncate+level 测试）。分支：`feat/industrialization`。

---



### 0.4 MVP 顺序优化达成（2026-08-26 续）

| 条目 | 实际达成 | 未达成 / 诚实边界 |
|---|---|---|
| M5 SST 读校验 | readBlock 边界+64MiB 上限；index CRC；lookup 返回 Status；DB::get 传播 Corruption/IOError；`test_sstable_corruption` | 未做 fuzz；open() 失败仍返回 nullptr（非 Status） |
| M6 Bloom | 孤儿 `.bloom` purge；compaction/orphan 删 SST 连带 `.bloom`；finish 按真实条数建 filter | 未做 T2.4 magic/version/CRC 格式升级；未测 10 万 key FP 率基准入库 |
| M7 RAG 快照 | `atomicWriteFile` tmp→fsync→rename→dir sync；SideIndex/HNSW 共用；空索引时 `RebuildFromStore` | 快照仍为 JSON（非二进制 v2）；启动不做 count 一致性强校验 |
| M10 删除一致 | DeleteDocument 后 `SaveSnapshotPrefix` 重写 col 快照 | 无独立 e2e kill/restart 自动化（需本地联调） |
| M8 网络边界 | 请求 64MiB / 写缓冲 256MiB 上限；EAGAIN 注册 EPOLLOUT；`ConnectionBoundsTest` | 未做 idle timeout、`--auth-token`、fuzz 30s |
| M9 MemTable O(log n) | `SkipList::findGreaterOrEqual` + Iterator；`MemTable::lookup` 用 max-seq seek；`MemTableIterator` 惰性（共享 ptr）；`test_memtable_seek` | 未做 bench_skip_list 对比入库；live MemTable 扫描非快照隔离（T2.2） |
| T2.3 TableCache | `TableCache` LRU(默认128)；Get/Iterator/compaction 走 cache；删文件 Evict+block invalidate；`test_table_cache` | 未做 /proc/self/fd 基准入库；容量未进 Options |
| T2.1 / T2.5 / 其余 T2 | 未做 | 按建议文档排在 M9/T2.3 之后 |

验收：`ctest` 全绿（含 MemTableSeek/TableCache）；`go test ./services/rag` ok；分支 `feat/industrialization`。

## 1. 执行纪律（所有阶段强制）

1. **复现先行**：每个修复先写一个"当前会失败"的测试（红），修完变绿才算完成。
2. **出错即重做**：若实现中发现设计本身有问题（不是笔误），`git revert` 干净回退该模块，
   在 `docs/ERRORLOG.md` 记录：现象 → 根因 → 本次教训 → 防重犯清单；然后重新设计再实现。
   禁止在错误实现上继续叠补丁。
3. **门禁**：每阶段结束必须 `make test` 全绿 + 无新增孤儿文件 + 无泄漏（ASAN 构建跑一遍），然后 `git tag`。
4. **提交粒度**：每个修复独立 commit，message 带 `fix:`/`feat:`/`perf:` 前缀和问题编号（如 `[P0-2]`）。
5. **诚实汇报**：每个任务完成后更新本文件对应条目状态，标注实际达成与未达成，不写预期以外的结论。

---

## 2. 阶段一：修改部分（P0/P1 正确性、数据安全、崩溃恢复）

> 目标：**任何进程被杀、断电、文件损坏场景下，数据不丢、不复活、不静默降级。**
> 本阶段不做性能优化，只做正确性。每个修复配回归测试。

### M1. [P0-1][已达标] 修复 compaction 删除数据复活 —— 重做 compactLevel 的 tombstone 规则

**现状问题**：`compaction.cpp:119` 对**任意层**合并都丢 tombstone；但旧值可能存在于更深层，
compaction 后 Get 会读到已删除的值。

**操作**：
1. 写复现测试 `tests/test_compaction_tombstone.cpp`：
   - 写 k→v1，flush；写 k→del，flush（产生两个 L0 SST）；手动触发 L0→L1 与 L1→L2，重启，断言 Get(k)=NotFound。
   - 当前该测试为红。
2. 修改 `mergeLevelFiles`：tombstone 仅在 **dst_level == max_level（最底层）** 时丢弃；
   其余情况作为普通记录写进输出文件。
3. 增加保守路径：合并前检查 dst_level 以下各层是否存在与输入 key 范围重叠的文件
   （用 index block 的 last_key 判定范围），若存在则同样保留 tombstone——为后续 M 阶段
   "范围感知删除"留接口。
4. 修正注释与 README 中"drop tombstones when merging into a deeper level"的错误说法。

**期望**：任何删除在 compaction 后仍然生效；空间回收只发生在安全时机。
**验收**：上述测试变绿；全量 ctest 绿；`make test` 绿。
**诚实边界**：这是 LevelDB 同款保守策略（LevelDB 只在最底层 drop）；不声称"范围感知删除"，
那是阶段二 T2.2 的内容。

### M2. [P0-2][已达标] WAL replay 截断损坏尾部 —— 重做 replay 的损坏处理

**现状问题**：`wal.cpp` replay 在 CRC/长度不符处 break，但不 ftruncate；
后续 append 写到损坏点之后 → 下次重启读不到损坏点之后的任何记录（静默丢新数据）。

**操作**：
1. 复现测试 `tests/test_wal.cpp` 增补：手写 WAL 文件（完整记录 + 故意截断的半条记录），
   replay 后检查文件被截断到最后一个完整记录；再 append 一条新记录，再次 replay 能读到旧记录 + 新记录。
2. 修改 `WAL::replay()`：顺序读，记录每个成功解析记录后的文件偏移 `good_offset`；
   遇到损坏立即停止，`ftruncate(fd, good_offset)`，再 `lseek(SEEK_END)`。
3. `DBImpl::recover()` 调用处无需改动（replay 内部自愈），但要在日志中输出
   `[WARN] truncated N bytes of torn WAL tail`（可观测性）。

**期望**：torn tail 被丢弃且不阻塞后续写入；重启后无数据丢失。
**验收**：新测试绿；模拟 kill -9 中断写（见阶段三 F1 注入框架）后重启数据一致。
**诚实边界**：只截断到最后一个**完整记录**；未 fsync 的半条记录本就是"未确认写入"，丢弃是正确的。

### M3. [P0-3][已达标] MANIFEST replay 截断 + 层数配置解耦 —— 重做 manifest 恢复路径

**现状问题**：
- `manifest.cpp:replay()` 与 WAL 同样的不截断问题。
- `Manifest::open` 硬编码 `levels_.assign(8,{})`；`Version()` 硬编码 `resize(7)`；
  `max_level` 配置变化会破坏恢复。

**操作**：
1. 复现测试：构造 MANIFEST（完整记录 + 半条损坏记录），open 后截断；
   再 recordAddFile，重启后能读到新记录。
2. `replay()`：同 M2 的 good_offset + ftruncate 逻辑。
3. 层数解耦：MANIFEST 首条记录改为 `kReset` + `level_count:u32`（或复用 kReset 记录后附加层数字段，
   向前兼容：无该字段视为默认 8）；`Version::restoreFrom` 用 manifest 提供的层数而非自身硬编码。
4. `Manifest::open` 中 `levels_.assign(8,{})` 与 `Version()` 的 `resize(7)` 全部改为由
   `Options::max_level` 推导，并在 `open()` 时校验 manifest 层数与配置一致（不一致时告警并按
   manifest 实际层数恢复，避免误删数据）。

**期望**：MANIFEST torn tail 自愈；max_level 配置不再有魔法数字；改配置不破坏恢复。
**验收**：新测试绿；用 `max_level=3` 打开 `max_level=7` 建的库不崩溃、数据可读（告警级别）。

### M4. [P0-4][已达标] rewriteSnapshot 的断电安全 —— 补目录 fsync

**现状问题**：`rewriteSnapshot` 有 rename 但无父目录 fsync；进程崩溃安全，但**断电**时
rename 可能未落盘，恢复时会回退到旧 MANIFEST（可接受）或出现 .new/.bak 混乱。

**操作**：
1. 新增 `fsyncDir(path)` 工具（open 目录 + fsync + close），在 `rewriteSnapshot` 的
   rename(.new→MANIFEST) 之后调用；同样在 rotateWal/新 WAL 创建后调用（WAL 文件创建即需目录项落盘）。
2. `recoverActivePathUnlocked` 的 .new/.bak 恢复逻辑保持，但增加日志输出恢复来源。

**期望**：断电场景下 MANIFEST/WAL 的目录项与数据一致。
**验收**：代码评审 + 注入测试（阶段三 F2 fault injection 中模拟 rename 后断电）。
**诚实边界**：断电测试在 WSL 环境无法真实模拟掉电，验收以"fsync 顺序正确 + 模拟进程杀"为准，
文档中如实标注。

### M5. [已达标 2026-08-26] [P1] SST 读取错误不再静默降级 + 读取边界防护 —— 重做 sstable_reader 校验

**现状问题**：
- `SSTableReader::get` 把 readBlock 的 IOError/Corruption 当 nullopt 返回 → 损坏文件 = 静默丢数据。
- `readBlock` 不校验 `h.offset + h.size ≤ file_size`；不校验 `payload_size`/`uncompressed_sz` 上限
  （损坏头可触发巨量分配）。
- index block 的 CRC 写入但读取不校验。

**操作**：
1. `readBlock` 增加：`h.offset + kSSTableBlockHeader + h.size ≤ file_size`（返回 Corruption）；
   `payload_size ≤ kMaxBlockPayload(64MB)`；`uncompressed_sz ≤ kMaxBlockUncompressed(64MB)`。
2. index block 读取后校验 CRC（与块一致）。
3. `SSTableReader::get/scan` 返回 `Status`；`DBImpl::get` 遇到 SST 读取错误时**返回 IOError**
   而不是 NotFound（上层 Go 服务转 500）。
4. `open()` 任何失败路径确保 fd 已关闭。

**期望**：损坏/截断的 SST 产生显式错误而非静默"找不到"。
**验收**：新增 `test_sstable_corruption.cpp`（翻转块内字节、截断文件、伪造巨大长度头），
断言返回 Corruption/IOError 且不崩溃、不巨量分配。

### M6. [已达标 2026-08-26] [P1] Bloom 孤儿清理 + 构造参数修正 —— 重做 bloom 生命周期

**现状问题**：
- `purgeOrphanSSTables` 只清 `.sst`，崩溃遗留的 `.bloom` 永远泄漏。
- compaction 删源文件时不删 `.bloom` 兄弟文件。
- `SSTableBuilder` 硬编码 `BloomFilter(10000, 0.01)`，与实际条目数无关。

**操作**：
1. `purgeOrphanSSTables`：遍历目录时把 `path + ".bloom"` 加入孤儿判定（无对应活 .sst 即删）。
2. `mergeLevelFiles` / `flushOne` 删除源文件时连带 `::unlink(path + ".bloom")`。
3. `SSTableBuilder`：block 写完后已知 entry_count（或 builder.add 计数），
   finish 时按实际条数构造 bloom——**两遍写入**：先写数据块，统计条数，再写 bloom 块（放 index 之前），
   或预算一个足够大的位数（按 block_size 反推上限条数）。选后者：`expected = entry_count` 精确值
   需要在 finish 时重建 bits——实现为：bloom 数据单独累积（先算条数再序列化）。
4. 格式升级见 T2.4（magic/version/CRC），本阶段先保证条数正确 + 文件生命周期正确。

**期望**：无孤儿 .bloom；FP 率与配置一致；小文件不浪费、大文件不漏检。
**验收**：构造孤儿场景测试；10 万 key 写入后实测 FP 率 ≤ 配置值 + 20%。

### M7. [已达标 2026-08-26 最小切片] [P1] RAG 向量索引崩溃一致性 —— 重做 SideIndex/HNSW 持久化与启动重建

**现状问题**：
- 向量只在内存 + 非原子 JSON 快照；服务崩溃于 SaveDocument 与 SaveSnapshot 之间 →
  minikv 有文档、向量索引没有 → 检索静默缺结果。
- RagKv.md §10.1 承诺"启动扫描 rag:chunk: 前缀重建缺失向量"，**代码未实现**。

**操作**：
1. 快照原子写：`SaveSnapshotPrefix` 改为 写 `*.idx.tmp` → fsync → rename → fsync 目录。
2. 快照格式 v2：`magic(4B) + version(2B) + dim(4B) + count(8B) + CRC32(4B) + 原始 float32 数组`
   （ID 用固定长度前缀编码，见 T2.12）；加载校验失败 → 返回错误并触发重建，绝不静默空索引。
3. 启动重建（新增 `RebuildFromStore(ctx, col)`）：列出 `rag:chunk:{col}:` 全部 chunk，
   对每个 chunk 的文本重新 embed（有 emb cache 则快），补齐向量，再写快照。
   启动流程：LoadSnapshot 成功且 count 与 chunk 数一致 → 直接用；不一致/损坏 → 重建。
4. `Ingest` 流程加固：SaveDocument 成功后先 `index.Add` 全部向量再置 status=success
   （已如此），但把"写快照"从"失败不阻塞"改为"失败记 warn + 触发后台重建标记"。

**期望**：任何崩溃点重启后，检索结果与 minikv 中的文档一致（不丢、不重、不静默空）。
**验收**：e2e 测试——ingest→kill→重启→retrieve 命中；删除快照文件→重启→自动重建→retrieve 命中。

### M8. [部分达标 2026-08-26] [P1] 网络协议健壮性 —— 重做 connection/server 边界

**现状问题**：
- 无最大请求大小上限：恶意客户端可无限填充 read_buf_（内存 DoS）。
- `onWritable` EAGAIN 后不注册 EPOLLOUT → 写缓冲堆积、连接卡死。
- 无 idle timeout（slowloris）。
- 服务端口无鉴权（工业内网可接受，但必须可配）。

**操作**：
1. `Connection`：`kMaxRequestSize`（默认 64MB，Options 可配），read_buf_ 超限直接断开。
2. `EventLoop` 支持 EPOLLOUT 事件注册/注销；`onWritable` EAGAIN 时注册 EPOLLOUT，
   写空后注销（edge 语义）；写缓冲上限（如 256MB）超限断开。
3. 空闲超时：连接注册到 loop 的 timer（如 60s 无完整请求断开），心跳/续期由读事件刷新。
4. `Server::processRequest` 增加防御性边界复检（rawData.size() 与 header 字段比对）。
5. 可选：`--auth-token` 启动参数，连接握手校验（默认关闭，部署时开启）。

**期望**：畸形/慢速/超大请求不能击穿服务；大响应不卡死连接。
**验收**：fuzz 测试（随机字节流喂 server，跑 30s 无崩溃/无 FD 泄漏）+ 慢客户端测试（暂停发送，
断言连接被断开）+ 大 value（100MB）测试返回显式错误而非 OOM。

### M9. [已达标 2026-08-26] [P1] MemTable 读路径 O(log n) —— 重做 skiplist 查找

**现状问题**：`MemTable::get` 调用 `entries()` 全表拷贝后线性扫描（O(n)/读）；
`MemTableIterator` 同样全拷贝。

**操作**：
1. `SkipList` 增加 `lowerBound(userKey)` 迭代器：从 head 按层下降找到第一个
   user_key == 目标（internal key 排序保证同 user key 的最大 seq 在最前）。
2. `MemTable::get(userKey, seq)` 改为：定位第一个匹配 internal key → 若 deletion 返回 nullopt，
   否则返回值；O(log n)。
3. `MemTableIterator` 改为直接持有 skiplist 迭代器（惰性遍历），删除 entries() 全拷贝路径
   （entries() 保留给 flush 用，但 iterator 不再用它）。

**期望**：读路径与 memtable 大小解耦；大 memtable 下 Get 延迟稳定。
**验收**：`bench_skip_list` 对比 + 正确性测试（含 deletion 边界、重复 key 版本顺序）。

### M10. [已达标 2026-08-26] [P1] 删除文档的端到端一致性 —— 审计 delete 路径

**现状问题**：`store.DeleteDocument` 只删 minikv KV；handler 侧是否同步清理 SideIndex
（ClearByPrefix）与快照未得到保证。

**操作**：
1. 审计 `services/rag/handler.go` 全部 delete 路径，列出 minikv / SideIndex / 快照 三处写点。
2. 统一封装 `CollectionService.Delete(col, docID)`：
   - minikv WriteBatch 删 KV（原子）→ 成功后 `index.ClearByPrefix(col+"/"+docID)` →
     快照原子重写 → 失败返回明确错误（幂等：重试不产生副作用）。
3. 补充 e2e 测试：ingest→delete→restart→检索为空。

**期望**：删除后三处一致，重启后无幽灵向量/幽灵 chunk。
**验收**：上述 e2e 测试绿。

---

## 3. 阶段二：完善部分（工业级性能、能力、可观测性）

> 目标：**在正确性的地基上，把关键路径提升到"性能有据、资源有界、故障可观测"。**
> 本阶段每项性能改动必须带基准数据（改前 vs 改后）。

### T2.1 流式 k-way merge compaction —— 重做 compaction 执行引擎

✅ 已达标（流式 MergingIterator + 块惰性 SST 迭代 + tmp→fsync→rename，2026-08-26）

**现状问题**：`mergeLevelFiles` 把所有源文件 KV 读进 `std::vector` 再排序 → 内存 O(输入总量)。

**操作**：
1. 基于现有 `MergingIterator` 改造多路流式合并：为每个源文件持有 `SSTableIterator`（按块惰性读），
   k-way 归并输出到 `.tmp` 文件；`builder.finish()` 后 fdatasync → rename 到最终路径 → fsync 目录。
2. 崩溃安全顺序：写 .tmp（可丢弃）→ rename → manifest add(dst) → manifest remove(src) → unlink 源
   （与 flush 顺序对齐；任一点崩溃，恢复期按 manifest 判定孤儿，见 M2/M3 已加固的截断逻辑）。
3. 输出按块大小裁剪：达到 block_size 就落块，流式写盘。
4. tombstone 规则沿用 M1 修复后的逻辑；新增输入文件数为 1 且已有序时直通（zero-copy 路径，可选）。

**期望**：compaction 内存 O(block×k)；1GB 数据合并内存峰值 < 200MB。
**验收**：基准测试对比（改前全物化 vs 改后流式）+ 阶段三 F1 崩溃注入全绿。

### T2.2 Snapshot API + snapshot 感知的删除裁剪

**现状问题**：引擎有 MVCC seq 但无 GetSnapshot/ReleaseSnapshot；compaction 丢弃 tombstone 时
完全不考虑活跃快照（M1 只做到"最底层才丢"，仍可能违反快照隔离）。

**操作**：
1. `DBImpl` 增加 `GetSnapshot()/ReleaseSnapshot()`：返回 seq；内部维护活跃快照的
   最小 seq（`oldest_snapshot_seq_`，原子）。
2. compaction 丢弃 tombstone 的条件升级为：`dst_level == max_level && seq < oldest_snapshot_seq_`
   才可丢；否则保留。
3. 读路径：`ReadOptions::snapshot` 传入后，MemTable/SST 查询以 snapshot seq 过滤
   （internal key 的 seq > snapshot_seq 视为不可见）。

**期望**：快照读隔离正确；删除空间回收不破坏活跃快照。
**验收**：新增 snapshot 测试（快照保持期间 compaction 触发，快照读仍见旧值）。

### T2.3. [已达标 2026-08-26] TableCache —— 文件句柄复用

**现状问题**：每次 Get/迭代/compaction 都 `SSTableReader::open` 新 fd + 重读 index block。

**操作**：
1. 实现 `TableCache`（基于现有 LRU 模板，key=sst_path，value=shared_ptr<SSTableReader>，
   容量按"文件个数"或"字节数"可配，默认 128 个文件）。
2. `DBImpl::get/newIterator/compaction` 统一走 `table_cache_->Get(path)`；
   compaction 删除源文件后 `table_cache_->Evict(path)`。
3. index block 解析移到 open 时一次（已如此），缓存命中时零解析。

**期望**：热点表读路径零 open/close；fd 数稳定 ≤ 缓存容量 + 常驻。
**验收**：基准测试 fd 数（/proc/self/fd）随查询量恒定。

### T2.4 Bloom 格式 v2 —— magic/version/CRC/条数精确

**现状问题**：bloom 独立文件无格式标识、无校验（M6 已修正条数与生命周期）。

**操作**：
1. 格式 v2：`magic("TKBF") + version(2B) + num_hashes(4B) + bits_per_key(4B) + entry_count(8B) + bit_size(8B) + CRC32(4B) + bits[]`。
2. `load()` 严格校验：magic/version/CRC/bit_size 上限（≤ 1GB）；失败返回 nullptr 且由
   上层把该 SST 视为损坏（与 M5 错误传播联动），**绝不静默当"无 bloom"**。
3. 按 M6 的精确条数构造；`mightContain` 不变。

**期望**：损坏 bloom 可检测；FP 率精确可控。
**验收**：损坏字节注入测试 + 10 万 key FP 率实测。

### T2.5 Write Stall —— 有界内存与 L0 背压

✅ 已达标（immutable/L0 Write Stall；Busy 可选，2026-08-26）

**现状问题**：immutable_list_ 无上限，写入持续快于 flush 时内存无限增长；
L0 文件数无背压，compaction 追不上时读放大爆炸。

**操作**：
1. `maybeFlush` 前检查：immutable 数量 ≥ `kMaxImmutable(默认4)` 时，`doWriteGroup` 在
   memtable 应用后**等待** flush 线程追平（condition_variable + 超时保护，返回
   Status::Busy/阻塞由配置决定）。
2. L0 文件数 ≥ `level0_stop_writes_trigger(默认 8)` 时阻塞写入直到 compaction 降下来。
3. 上述阈值全部进 Options。

**期望**：任意写压下 RSS 有界；L0 文件数有界。
**验收**：压测（写满 4 线程 60s）观察 RSS 曲线有界 + L0 ≤ 8。

### T2.6 引擎可观测性 —— 内嵌指标 + Prometheus 端点

✅ 已达标（最小：原子计数器 + /metrics+/healthz + scraper/prometheus 接线；无直方图/仪表盘 JSON，2026-08-26）

**现状问题**：README 声称 Prometheus/Grafana/Jaeger，但 C++ 引擎零指标；
observability 服务刮的是 Go 层，仪表盘上看不到引擎真实状态。

**操作**：
1. 新增 `src/utils/metrics.h/cpp`：原子计数器（puts/gets/scans/batches、QPS 滑动窗口、
   延迟直方图（固定桶）、compaction 次数与字节、flush 次数、cache hit/miss、
   memtable 大小、immutable 数量、L0 文件数、磁盘占用）。
2. 引擎内嵌 HTTP 端点（复用现有 event_loop 或最小 socket）：`GET /metrics` 输出 Prometheus 文本格式
   （`titankv_engine_*` 前缀），`GET /healthz`。
3. `services/observability/scraper.go` 增加对 minikv 端点的抓取；`pkg/metrics` 汇总。
4. `deploy/dev/prometheus.yml` 加 minikv job；grafana dashboard JSON 入库（docs/ 下）。

**期望**：仪表盘能实时看到引擎 QPS/P99/compaction/cache 指标；面试可现场演示。
**验收**：curl /metrics 有数据；压测下 QPS 曲线与 P99 可观测；面板截图入库 docs/。

### T2.7 引擎配置化

**现状问题**：Options 硬编码默认值，server 只认 5 个命令行参数。

**操作**：
1. 命令行 + 环境变量（`MINIKV_*`）覆盖全部 Options：memtable_size、block_size、
   lru_cache_capacity、max_level、L0 触发/停止阈值、bloom fp 率、压缩算法、
   max_request_size、table_cache 容量、immutable 上限。
2. 启动时打印生效配置（LOG_INFO 逐项）。

**期望**：不改代码即可调优；面试可演示"同一二进制不同配置"。
**验收**：不同配置启动对比基准数据。

### T2.8 Scan/DeleteRange 分页与分批 —— 服务端内存有界

**现状问题**：`kScan`/`kDeleteRange` 服务端全量物化。

**操作**：
1. 协议扩展：Scan 请求 value 字段携带 `limit:u32`（0=不限，但服务端默认 10000 上限），
   响应带 `has_more` 标记；Go client 增加 `ScanPage(start, end, limit) (items, nextKey, error)`。
2. `kDeleteRange` 分批：每批 ≤ 10000 个 key 一个 WriteBatch，循环直到扫完；
   批量间不持锁（每次 newIterator 重新定位，容忍并发写，语义为"尽力删除"并在文档注明）。
3. RAG store 的分页遍历（ListChunks/ListChat/ListDocuments）改用 ScanPage。

**期望**：100 万 key 全表扫描服务端内存 < 50MB；DeleteRange 不阻塞写路径。
**验收**：压测 + 内存观测。

### T2.9 RAG Chunker 升级 —— 软边界 + 精确 token 估算 + 文档类型路由

**现状问题**（详见代码审计）：
- token 估算 `runes/2` 对英文/代码严重失真；
- 滑窗在任意字符处硬切，破坏句子/段落；
- 只识别 `##`/`###`，无 `#`/`####`、无标题层级路径；
- V2（PDF/代码/FAQ 特化）只存在于 RagKv.md。

**操作**：
1. 估算器 `estimateTokens(s)`：按 UTF-8 分类统计——ASCII 字母/数字 4 字节 ≈ 1 token、
   CJK 1.5 字 ≈ 1 token、空白/标点按 0.5 折算；提供 `TokenEstimator` 接口便于替换
   （可接入本地轻量 BPE 词典，但不默认依赖）。
2. 软边界切分 `slideWindowWithBoundaries`：优先在句子边界（`。！？.!?\n` 段落/列表项）收尾，
   找不到边界才在 `size×0.9` 处硬切；overlap 以完整句子为单位向后延伸。
3. 标题层级：识别 `#{1,6}`，heading 存层级路径（`3.2 存储引擎 → 3.2.1 WAL`），
   顶层 `#` 不再被忽略。
4. 文档类型路由 `ChunkerFor(source, mime)`：`.md/.markdown` 用标题感知；
   `.pdf` 走页码+段落；代码文件按函数/类边界（启发式：行首 `func/def/class/void/...` + 空行分组）；
   FAQ 按 `Q:`/`A:` 对。RAG_INGEST 接口已有 source 字段，无需改协议。
5. 全量重切后必须重嵌向量（ingest 幂等：content_hash 相同则跳过——注意 hash 计算要在
   切分之前，切分器变化不改变 hash，所以**新增 `chunker_version` 字段**入 DocumentMeta，
   版本不一致时强制重切重嵌）。

**期望**：同一评测集（阶段三建立）Recall@K/MRR 显著提升；chunk 边界符合语言直觉。
**验收**：新增 chunker 单测（中英混排、长段无标点、markdown 层级、代码文件、FAQ）；
   eval 对比基线数据入库 docs/benchmarks/。

### T2.10 向量索引升级 —— 归一化预计算 + HNSW 可靠化

**现状问题**：
- TopK 每对向量重算 L2 范数（浪费 2× 计算）；HNSW 无 ef 参数、Delete 留悬垂边、
  邻居选择粗糙、无持久化图。

**操作**：
1. `SideIndex.Add` 时预归一化（保存归一化副本）；`TopK` 只算点积；metric 支持 cosine/dot/l2
   （l2 转平方距离，均预计算）。
2. HNSW 改进（诚实范围，不引入外部库——原计划 hnswlib-go 可作后续对比项）：
   - `efConstruction`/`efSearch` 可配（默认 200/100）；`M=16` 保持。
   - Add 时对每个候选邻居做**双向修剪**（超出 M 时按相似度截断，保持邻居质量）；
   - Delete 改为 mark-deleted + 定期清理（避免大图重建）；查询跳过 deleted 节点，
     且从其邻居集合剔除（惰性）；
   - 图持久化：SaveSnapshot 同时存邻接表（二进制，格式同 T2.12 快照规范），
     MergeSnapshot 重建图。
3. `NewVectorIndex` 选择逻辑加 env `RAG_INDEX_TYPE=hnsw|brute`；默认 hnsw（N≥1 万），
   启动时 N < 1 万自动回退 brute（诚实：HNSW 小数据无优势）。
4. emb cache/chat/qlog 增加 TTL 与容量上限：`rag:cache:emb:` 前缀加 `created_at`，
   后台任务清理超过 `RAG_CACHE_TTL_HOURS`（默认 72h）或容量上限的条目（分批 DeleteRange）。

**期望**：TopK 延迟（10 万向量）降至可交互；召回率 vs 暴力 ≥ 0.95；索引持久化可靠。
**验收**：benchmark 脚本（召回率 + 延迟）数据入库；e2e 重启恢复测试全绿。

### T2.11 分布式层加固（诚实范围，明确边界）

**现状问题**：README 自标"教学实验"：raft 单节点、一致性哈希分片无迁移、无故障转移。

**操作**（不宣称生产级，做到"可演示、可讲故事"）：
1. Raft 真实多节点：`distributed/` 增加 3 节点启动脚本（deploy/dev/docker-compose 或本机多端口），
   集成测试：leader 下线 → 重新选举 → 写入仍可用；网络分区 → 恢复。
2. 分片路由表：`gateway/shard.go` 升级为版本化路由表（`shard_map` + `version`），
   元数据存 raft（meta 服务）；节点增删时路由表版本递增。
3. 数据迁移协议（简化实现）：按 key 范围搬迁（ScanPage + WriteBatch 逐批 + 双写窗口），
   迁移期间新旧节点同时可读，完成标记后切换路由表版本。
4. 幂等写：请求带 `req_id`，写入层按 `req_id` 去重（minikv 侧支持按 key 的
   `idempotency:` 前缀标记，简单实现即可）——为重试/迁移打底。
5. 演示剧本入库 `docs/demo-distributed.md`。

**期望**：现场可演示 3 节点 raft 故障切换 + 分片迁移；面试能讲清"当前做到哪、为什么没做全"。
**诚实边界**：明确标注"KV 分片迁移为最终一致性（双写窗口），raft 仅管元数据/路由表"；
   不做跨节点事务/强一致分片，README 相应措辞同步修正（防虚构风险）。

### T2.12 RAG 快照二进制规范（与 T2.10 联动）

**操作**：统一向量快照/图快照二进制格式：
`magic("TKVX") + version + col_name_len + col_name + dim + metric + count + crc + [chunk_id_len + chunk_id + float32[] × count]`。
写入流程：tmp → fsync → rename → dir fsync；读取校验失败返回 ErrCorruptSnapshot，
上层触发重建（M7）。JSON 格式废弃并迁移。

---

## 4. 阶段三：最终迭代成功（测试体系、压测、文档、演示）

> 目标：**让整个系统在任何故障剧本下可验证、性能有数据、故事能自圆其说。**

### F1 崩溃注入框架（kill -9 随机点）

**操作**：
1. `scripts/crash_inject.sh`：后台灌数据（多线程随机 put/del/scan），随机时刻 kill -9 minikv_server，
   重启后做一致性校验（预写检查点集合 vs 恢复后集合：已 fsync 的写入必须都在，未确认写入可缺失）。
2. 注入点覆盖：flush 中、compaction 中、manifest rewrite 中、WAL rotate 后、RAG ingest 各步骤。
3. 结果归一：每次注入 → 重启 → 校验 → 报告（通过/失败 + 复现日志）。

**期望**：任何注入点重启后：数据不丢（已确认写入）、不复活（删除生效）、能继续写入。
**验收**：连续跑 200 轮注入 0 失败；失败即按 §1 纪律处理。

### F2 Fault Injection（IO 错误模拟）

**操作**：
1. 在 `env_posix.cpp` 增加测试钩子：按文件类型（.sst/.log/MANIFEST/.bloom/.idx）注入
   写入失败/读取失败/截断（环境变量 `MINIKV_FAULT_INJECT=write:0.01:MANIFEST`）。
2. 全部 P0 路径（WAL append、flush、compaction、manifest、快照）用 fault 场景验证
   **错误被显式传播**（不崩溃、不静默丢数据）。

**期望**：磁盘满/写失败/读损坏场景行为可预期。
**验收**：fault 场景测试全绿。

### F3 并发与压力测试

**操作**：
1. 引擎级：多线程（8）随机 put/get/del/scan 30 分钟，期间周期校验线性化约束
   （简单模型：读到的值必须是某个已确认写入的值）；ASAN/TSAN 构建各跑一遍。
2. 端到端：`client-go` SDK 并发 + RAG ingest/retrieve 混合压测。
3. 数据入库 `docs/benchmarks/`：QPS/P99/内存/磁盘放大曲线（vs memtable/block/cache 配置矩阵）。

**期望**：无数据竞争、无泄漏、无崩溃；性能曲线可作为面试论据。
**验收**：TSAN 0 报告；压测 30min RSS 有界。

### F4 性能基准（改前 vs 改后）

**操作**：固定脚本复测每个性能项的 before/after：
- 读路径（T2.3 TableCache、T2.1 流式 compaction、M9 MemTable O(log n)）
- 写路径（组提交吞吐、T2.5 write stall）
- 检索（T2.10 归一化/HNSW）
- 扫描（T2.8 分页）
表格入库 `docs/benchmarks/engine.md`。

### F5 文档与面试叙事收口

**操作**：
1. 更新 README：修正"完整分布式/可观测性"过度宣称（防虚构风险）；
   新增"工业化改造记录"章节指向本计划与 benchmarks。
2. 编写 `docs/interview.md`：每个核心组件一页"为什么这么设计 / 换个场景怎么改 / 已知局限"，
   按面试追问路径组织（写入→WAL→memtable→flush→SST→bloom→cache→compaction→crash→
   RAG 检索→分布式→可观测）。
3. `RagKv.md` 与 `docs/layers/*` 同步最新实现（含未提交的 1329 行文档改动，一并评审提交）。
4. 提交当前未提交的 `docs/layers/` 改动（经评审）。

**期望**：任何面试追问都有代码+数据+诚实边界支撑；README 无一句不可兑现。
**验收**：README 逐句核对；interview.md 完成；git 历史干净（按 §1 门禁打 tag）。

### F6 一键演示

**操作**：`scripts/demo.sh` 串起：启动全栈 → 写入 10 万 key → 仪表盘截图 →
RAG ingest 中文文档 → 提问检索 → kill -9 → 重启 → 数据校验 → 故障切换演示（T2.11）。

**期望**：面试现场 5 分钟完整故事。
**验收**：演示脚本在干净环境跑通。

---

## 5. 阶段依赖与工作量（诚实估算）

| 阶段 | 依赖 | 估算（人日） | 里程碑 |
|---|---|---|---|
| 一 M1-M10 | 无 | 5-7 | `v2.0.0-industrial-core`（正确性地基） |
| 二 T2.1-T2.12 | 一 | 10-14 | `v2.1.0-industrial-perf`（性能/能力） |
| 三 F1-F6 | 一+二 | 5-8 | `v2.2.0-industrial-final`（验收/叙事） |

> 估算按单人、每天 4 小时有效深度工作；只做下限承诺，不排虚高里程碑。

---

## 6. 错误记录规范（ERRORLOG.md 模板）

每项失败/重做在此登记：

```markdown
## [编号] 日期 — 模块 — 现象
- 复现路径：
- 根因分析（是什么设计假设错了）：
- 为什么不能打补丁解决：
- 重做方案（简洁，指向本计划条目）：
- 防重犯清单：
  1. ...
```

**初始登记（本计划撰写时已核实的教训）**：
- 曾发生：recover() 曾 append 空 kReset 导致第二次重开丢 SST 引用（测试 test_db_reopen 已回归修复）。
  教训：**任何"重置类"持久化操作必须先用快照重写替代**——本计划 M3/M4 全面沿用该原则。

---

## 7. 开始执行顺序（第一个 1 小时）

1. `git checkout -b feat/industrialization`
2. 提交本计划文档与 ERRORLOG 骨架
3. 按 M1 → M2 → M3 → M4 顺序写复现测试（红）→ 修复（绿），每项独立 commit
4. 阶段一完成：全量测试 + ASAN 构建 + `git tag v2.0.0-industrial-core`
