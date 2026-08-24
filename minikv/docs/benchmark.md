# MiniKV 压测报告

> 实测日期: 2026-08-07 · WSL2 · 本地 loopback TCP（非生产数字）
> 脚本: `scripts/bench_minikv.sh`
> 复现: `make cmake-build && ./build/minikv/minikv_server --port 8888 --db /tmp/mk &`
>       `./scripts/bench_minikv.sh 127.0.0.1:8888 5000`

## 环境

| 项 | 值 |
|----|----|
| CPU | 见下方 `nproc` / model（WSL 虚拟核） |
| 磁盘 | WSL 虚拟盘（`/tmp`） |
| 编译 | g++-12, CMake Release |
| 协议 | MiniKV 原生 TCP（magic=`0x4D4B`） |
| Value | 64B |
| N | 5000 |

## 端到端 TCP Benchmark（本机实测）

| 操作 | 吞吐 (ops/s) | 平均延迟 |
|------|-------------|----------|
| PUT  | **10961** | 91.2 µs |
| GET  | **3352** | 298.4 µs |

说明：上表是 **2026-08-07 单线程 Reactor** 时的单连接同步请求。此后已改为 main + sub Reactor（默认 `--io-threads 4` / `--biz-threads 4`），并接入 BlockCache、MemTable `shared_ptr` 锁外读、L1+ Compaction、waitFlush。重跑：

```bash
make cmake-build
./build/minikv/minikv_server --port 8888 --db /tmp/mk --io-threads 4 &
./scripts/bench_minikv.sh 127.0.0.1:8888 5000
```

## SkipList / WAL 分项

（可选后续用 `minikv/benches/` Google Benchmark 补齐）

| 项 | 状态 |
|----|------|
| SkipList microbench | 有 bench 目标，未写入本表 |
| WAL fsync ON/OFF | 未单独测 |

## 诚实边界

- 未做多连接/流水线；未与 RocksDB 同条件对比
- Data 服务经 Go HTTP 网关再转发时延迟会更高（另测）
- Compaction 已实现真实 L0→L1 合并，但非生产 leveled 全套
