# MiniKV 设计文档

## 1. LSM-Tree 架构

MiniKV 采用 Log-Structured Merge-Tree (LSM-Tree) 架构，核心思想：

- **写入路径**: WAL → MemTable → Immutable MemTable → SSTable (flush)
- **读取路径**: MemTable → Immutable → L0 SSTables → L1+ SSTables
- **后台**: Compaction 合并 SSTable，控制读放大和空间放大

## 2. 核心组件

### SkipList (MemTable)
- 概率数据结构，期望 O(logN) 查询/插入
- p=0.5, maxLevel=32
- 读写锁保证并发安全

### Bloom Filter
- Double hashing (MurmurHash2)
- 每个 SSTable 对应一个 Bloom Filter

### WAL
- 顺序追加写；每代 MemTable 一个 wal-<N>.log
- 格式: [crc(4B)][len(4B)][data]
- MemTable 满：开新 WAL 文件；flush 后删除旧 WAL
- 崩溃恢复: 按编号重放全部残留 wal-*.log

### SSTable
- Data Block (4KB, 前缀压缩) → Meta Block (Bloom) → Index Block → Footer (48B)
- 查找: Bloom → 二分 Index → 读 Data Block

### Compaction
- L0→L1: 处理 key 范围重叠
- Ln→Ln+1: 归并排序，保留最新版本
- Tombstone 在最后一层真正删除

## 3. 文件格式

(MiniKV/src/core/sstable_builder.cpp 中实现)

## 4. 并发模型

- 读写锁: MemTable (shared_mutex)
- 后台线程: flush + compaction (ThreadPool)
- 网络层: **主从 Reactor**（epoll LT）
  - Main Reactor（主线程）：只 `accept`，新连接 round-robin 分给 Sub Reactor
  - Sub Reactor（`--io-threads`，默认 4）：各自一条线程 + 一个 epoll，负责已连接 fd 的读写
  - 跨线程投递用 `eventfd` 唤醒 + `runInLoop`（禁止在非 loop 线程改 `callbacks_`）
  - 连接仍只挂 `EPOLLIN`（LT）；`--io-threads 0` 可退回单线程（accept+IO 都在 Main）
  - 业务池（`--biz-threads`，默认 4）：`processRequest`/`db_` 在池里跑，响应 `queueInLoop` 回所属 Sub 再 write；同连接串行（`in_flight_`）保序。`--biz-threads 0` 仍在 Sub IO 线程同步调 `db_`
  - Put 走 `write_mutex_`；Get 读 MemTable 指针时也短持这把锁，避免 flush 交换 `unique_ptr`
