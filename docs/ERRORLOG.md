# TitanKV 错误记录（ERRORLOG）

> 纪律：出错不修补。回滚该模块重做；在此登记现象 → 根因 → 教训 → 防重犯。
> 对应计划：`docs/industrialization-plan.md`

---

## [ENG] T2.1/T2.5/T2.6 industrialization (2026-08-26)

- **T2.1**: compaction `mergeLevelFiles` 改为 MergingIterator 流式写出；`SSTableIterator` 按块惰性加载；产物 tmp→fsync→rename→dir fsync。
- **T2.5**: `waitUntilWritable` 按 immutable/L0 阈值阻塞或返回 `Status::Busy`。
- **T2.6(最小)**: `EngineMetrics` + `--metrics-port` HTTP `/metrics` `/healthz`；observability scraper 探测 minikv。
- **诚实边界**: T2.6 无延迟直方图/Grafana JSON；T2.2 live MemTable 快照隔离未做。

## [E0] 2025-08-25 — Manifest — 空 kReset 导致二次重开丢 SST

- 复现路径：`test_db_reopen` / 旧 `recover()` 每次 open append 空 `kReset`
- 根因分析：把「重置类」持久化当成追加一条空记录；二次 replay 越过 reset 后看不到 SST 引用
- 为什么不能打补丁解决：空 reset 语义本身错误，叠补丁只会掩盖恢复契约
- 重做方案：`rewriteSnapshot()` 快照重写替代空 kReset（已落地）
- 防重犯清单：
  1. 任何重置类持久化必须先快照重写
  2. 多轮 reopen 回归必进 CI

---

## [E1] 2026-08-26 — Compaction/Get — 删除后数据复活（M1 发现）

- 复现路径：`test_compaction_tombstone` — put→flush→compact 到 L1 → del→flush→compact L0；Get 读到旧值
- 根因分析：
  1. `mergeLevelFiles` 在任意目标层都丢弃 tombstone（`compaction.cpp`）
  2. `DBImpl::get` / `MemTable::get` / `SSTableReader::get` 把 tombstone 与 miss 都映射为「继续找下层」，跨文件/跨层时旧值复活
- 为什么不能只改 drop 条件：保留 tombstone 后若 Get 仍把 tombstone 当 miss，L1 上「旧值 SST + 新 tombstone SST」并存时仍会命中旧值
- 重做方案：M1 — 仅 `dst_level == max_level` 时丢 tombstone；Get 路径区分 miss / value / tombstone，命中 tombstone 立即 NotFound
- 防重犯清单：
  1. 点查 API 必须三态，禁止 tombstone≈miss
  2. 压缩丢 tombstone 的条件与读路径语义一起改、一起测
  3. 不声称范围感知删除（那是 T2.2）


## [E2] 2026-08-26 — SST 损坏被当成 NotFound（M5）

- 现象：`readBlock` 失败时 `lookup` 返回 miss，上层继续扫更旧 SST / 最终 NotFound。
- 根因：点查只有二态，没有把 Corruption/IOError 与 miss 分开。
- 教训：损坏必须显式错误；Go Data 应映射 500，而非空结果。
- 防重：`SSTableCorruptionTest` + DB get 传播。

## [E3] 2026-08-26 — 孤儿 .bloom 泄漏（M6）

- 现象：purge 只认 `.sst`；compaction unlink 不删 sibling `.bloom`。
- 根因：生命周期与 SST 脱钩。
- 教训：成对文件必须成对删；filter 条数勿硬编码。
- 防重：`BloomLifecycleTest`。

## [E4] 2026-08-26 — RAG 快照非原子 + 删除不重写（M7/M10）

- 现象：`os.Create` 直写 JSON；Delete 只清内存索引，重启可从旧 `.idx` 复活向量。
- 根因：旁路索引与 KV 删除未绑在同一持久化步骤。
- 教训：旁路状态也要原子落盘；删文档必须重写快照。
- 防重：`atomicWriteFile` + DeleteDocument `SaveSnapshotPrefix`。


## [E5] 2026-08-26 — MemTable Get 全表拷贝扫 O(n)（M9）

- 现象：`lookup` 调 `entries()` 再线性扫；大 MemTable 下读延迟随条目数线性涨。
- 根因：SkipList 已按 InternalKey 有序，却没用 lowerBound；Iterator 也物化全表。
- 教训：有序结构必须走 seek；flush 才需要 `entries()` 快照。
- 防重：`test_memtable_seek`（多版本/tombstone/大表/惰性 iterator）。


## [E6] 2026-08-26 — 每次 Get 重复 open SST + 共享 reader 的 lseek 竞态（T2.3）

- 现象：热点路径每次 `SSTableReader::open` 新 fd + 重读 index。
- 根因：无 TableCache；BlockCache 只缓存块，不缓存 reader。
- 教训：文件级句柄复用与块缓存分层；compaction 删文件必须 Evict。
- 防重：`TableCacheTest` + Get/Iterator 统一 `table_cache_->get`。
- 连带：共享 `SSTableReader` 后 `lseek`+`read` 并发会 CRC 错读 → 一律改 `pread`。
