# MiniKV 压测报告

> 最新实测: **2026-08-29**（E2 kBatch） · WSL2 · 本地 loopback TCP（非生产数字）  
> 脚本: `scripts/bench_minikv.sh`  
> 复现:
>
> ```bash
> make cmake-build
> ./build/minikv/minikv_server --host 127.0.0.1 --port 8888 --db /tmp/mk \
>   --io-threads 4 --biz-threads 4 --metrics-port 9091 &
> ./scripts/bench_minikv.sh 127.0.0.1:8888 5000 100
> ```

## 环境（2026-08-29）

| 项 | 值 |
|----|----|
| Host | LAPTOP-DPSBHS3R / WSL2 |
| CPU | 12th Gen Intel i5-12450H（`nproc=4`） |
| 磁盘 | WSL 虚拟盘（`/tmp`） |
| 编译 | g++-12, CMake Release |
| 协议 | MiniKV 原生 TCP（magic=`0x4D4B`） |
| 线程 | `--io-threads 4` / `--biz-threads 4` |
| Value | 64B |
| N | 5000 |
| WriteOptions.sync | **true**（网络层默认；每次 Put/每个 batch **一次** fsync） |

## 端到端 TCP Benchmark（E2，2026-08-29 实测）

单连接同步请求；Put/Get 为逐条 cmd；kBatch 为 `Cmd::kBatch=5`（与 Go `MiniKVClient.WriteBatch` / RAG WriteBatch 同路径）。

| 操作 | 吞吐 (ops/s) | 平均延迟 | 备注 |
|------|-------------|---------|------|
| PUT（单条） | **~395** | ~2.5 ms | 每次 1 RTT + 1 fsync |
| GET（单条） | **~620** | ~1.6 ms | 每次 1 RTT |
| **kBatch Put**（batch=100） | **~20000–21000** | ~47–50 µs/键 | 50 RTT / 5000 keys；**一批一次** fsync |
| kBatch Put（batch=50） | **~12700** | ~79 µs/键 | 100 RTT / 5000 keys |

**结论（可口述）**：在默认 `sync=true` 下，kBatch(100) 比单条 Put 约 **50×** ops/秒（摊销 RTT + 批内共享一次 WAL sync）。RAG 入库 / Wiki 落盘走的就是这条 TCP WriteBatch 路径。

原始输出摘录（稳定性第二跑）：

```text
n=5000 value=64B batch_size=100 batch_keys=5000 batches=50
put:       391 ops/s  avg=2557.6 us
get:       633 ops/s  avg=1580.8 us
batch_put: 20201 ops/s  avg_per_key=49.5 us  rtt=202 batch/s  avg_batch=4.95 ms
```

## 历史对照（2026-08-07，仅供参考）

当时为**单线程 Reactor**、未包含 kBatch；机器/配置不同，**不可直接对比**。

| 操作 | 吞吐 (ops/s) | 平均延迟 |
|------|-------------|---------|---|---|---|
| PUT  | 10961 | 91.2 µs |
| GET  | 3352 | 298.4 µs |

## SkipList / WAL 分项

| 项 | 状态 |
|----|------|
| SkipList microbench | `minikv/benches/` 有目标，未写入本表 |
| WAL fsync ON/OFF 对照矩阵 | 未单独测；上表均为 sync=true |

## 诚实边界

- 单连接、无流水线；未与 RocksDB 同条件对比
- 单条 Put 慢主因是 **每次 fsync**（`WriteOptions.sync=true`），不是「引擎变慢」的唯一解释
- Data / Gin 再转一层 HTTP 时延迟更高（另测）
- Compaction 已有真实 L0→L1 / L1→L2（E1），但非生产 leveled 全套
