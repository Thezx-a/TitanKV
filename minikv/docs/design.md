# MiniKV 设计文档

## 1. LSM-Tree 架构

- **写入路径**: WAL → active MemTable → immutable MemTable(s) → SSTable (flush) → Compaction
- **读取路径**: active MemTable → immutable 列表 → L0 → L1+（Bloom + BlockCache）
- **后台**: flush 线程 + Compaction，控制读放大/空间放大

## 2. 核心组件

### MemTable / SkipList
- SkipList：p=0.5, maxLevel=32，`shared_mutex` 保护表内并发
- `DBImpl` 用 `std::shared_ptr<MemTable>` 持有 active + immutable
- Get/Iterator：锁内拷贝 `shared_ptr` 快照，**锁外**查表；flush 线程持同一引用做 IO

### BlockCache
- 解压后的 data block LRU，key=`(sst_path, offset)`，容量=`Options::lru_cache_capacity`（默认 8MB）
- Compaction/删除 SST 时 `invalidatePath`

### Bloom Filter
- Double hashing (MurmurHash2)，每张 SST 一份

### WAL
- 每代 MemTable 一个 `wal-<N>.log`；格式 `[crc][len][data]`
- flush 成功后删除对应 WAL；崩溃按编号重放残留文件

### SSTable
- Data Block（可 Snappy/Zstd）→ Index → Footer
- 查找: Bloom → 二分 Index → BlockCache / 读盘解压

### Compaction
- L0→L1 处理重叠；Ln→Ln+1 归并保留最新版本；tombstone 在底层删除

### MVCC
- InternalKey = `user_key | seq | type`；全链路 string 编码 + 比较器

## 3. 并发模型（网络）

- **主从 Reactor（epoll LT）**
  - Main：只 `accept`，round-robin 分给 Sub
  - Sub：`--io-threads` 默认 4；连接挂 `EPOLLIN`（LT）
  - 业务池：`--biz-threads` 默认 4；`processRequest` 在池内，`queueInLoop` 回 Sub 写
  - `--io-threads 0` / `--biz-threads 0` 可退化
- 写入串行：`write_mutex_`（含 group commit）；读路径尽量不长时间持锁

## 4. 与 TitanKV 其它层的边界

- `minikv_server` 默认 TCP `:8888`；Go Data/RAG 经 `MINIKV_ADDR` 访问
- 不负责 JWT/RBAC；那是 Gin 网关的事
- skynet 前置是 **另一条线**（ET + 协程），不要和本网络层混讲
