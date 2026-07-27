# TitanKV 实战课程 / TitanKV Hands-on Course

> 从零基础到能独立复现一个分布式 KV 存储系统，逐层递进，紧扣源码。
>
> From absolute zero to reproducing a distributed KV store on your own, layer by layer, tightly bound to the source.

**语言 / Language：** [中文大纲](./zh/README.md) ｜ [English Syllabus](./en/README.md)

---

## 课程定位 / Course Positioning

本课程以 **TitanKV**（一个从零实现的分布式 KV 存储平台）为唯一教材，覆盖从 C++ 语法到分布式全栈的完整工程化路径，共 **15 个模块（Module 00-14）**。
This course uses **TitanKV** (a distributed KV storage platform built from scratch) as its only textbook, covering the complete engineering path from C++ syntax to full-stack distributed systems, across **15 modules (Module 00-14)**.

- **存储引擎层 / Storage Engine**：C++17 LSM-Tree（WAL / MemTable / SSTable / Compaction / BloomFilter / MVCC）
- **网络层 / Networking**：C++20 协程网络库（epoll / Executor / Task / HTTP / 反向代理 / Reverse Proxy）
- **分布式层 / Distributed**：Go 微服务 + Raft 共识 + 一致性哈希分片 / Go µServices + Raft consensus + consistent-hash sharding
- **应用层 / Application**：Next.js 管理控制台 + 可观测性 / Next.js admin console + observability（Prometheus / Grafana / Jaeger）
- **复现篇 / Reproduction**：Module 14 从空目录开始复现整个项目 / Rebuild the entire project from an empty directory in Module 14

每个模块统一结构：**背景与动机 → 核心知识 → 内容详解 → 思考题 → 动手题 → 自检**，并配套中英双语文档。
Every module follows a unified structure: **Background & Motivation → Core Knowledge → Deep Dive → Thinking Questions → Hands-on Exercises → Self-Check**, with bilingual docs.

---

## 模块地图 / Module Map

| # | 模块 / Module | 层 / Layer | 阶段 / Stage | 对应源码 / Source | 状态 / Status |
|---|---|---|---|---|---|
| 00 | 跨平台环境搭建 / Cross-Platform Env Setup | 准备 / Prep | 1 准备 / Prep | `CMakeLists.txt` / `Makefile` / `minikv/CMakePresets.json` / `skynet/CMakePresets.json` | 🚧 进行中 / In progress |
| 01 | 环境搭建与项目概览 / Env Setup & Project Overview | 基础 / Foundation | 1 准备 / Prep | `CMakeLists.txt` / `Makefile` / `go.mod` / `README.md` / `docs/REFACTORING.md` | ✅ 已完成 / Done |
| 02 | C++ 核心语法 / C++ Core Syntax | 基础 / Foundation | 2 基础 / Foundation | `minikv/src/utils/` | ✅ 已完成 / Done |
| 03 | 现代 C++ 与并发 / Modern C++ & Concurrency | 基础 / Foundation | 2 基础 / Foundation | `minikv/src/core/skip_list.h` / `minikv/src/utils/thread_pool.h` | ✅ 已完成 / Done |
| 04 | Go 与 TypeScript 基础 / Go & TS Basics | 基础 / Foundation | 2 基础 / Foundation | `go.mod` / `services/` / `web/` | ✅ 已完成 / Done |
| 05 | 跳表与有序结构 / SkipList & Ordered Structures | 数据结构 / Data Structures | 3 数据结构 / Data Structures | `minikv/src/core/skip_list.h` | ✅ 已完成 / Done |
| 06 | 布隆过滤器与哈希 / BloomFilter & Hashing | 数据结构 / Data Structures | 3 数据结构 / Data Structures | `minikv/src/core/bloom_filter.h` / `minikv/src/utils/hash.h` | ✅ 已完成 / Done |
| 07 | LSM-Tree 存储引擎 / LSM-Tree Engine | 存储引擎 / Storage Engine | 4 存储引擎 / Storage Engine | `minikv/src/core/{wal,memtable,sstable*}.cpp` | ✅ 已完成 / Done |
| 08 | Compaction 与 MVCC / Compaction & MVCC | 存储引擎 / Storage Engine | 4 存储引擎 / Storage Engine | `minikv/src/core/{compaction,internal_key,manifest}.cpp` | ✅ 已完成 / Done |
| 09 | epoll 与 C++20 协程 / epoll & C++20 Coroutines | 网络 / Networking | 5 网络 / Networking | `skynet/src/net/` / `skynet/src/core/` | ✅ 已完成 / Done |
| 10 | HTTP 与反向代理 / HTTP & Reverse Proxy | 网络 / Networking | 5 网络 / Networking | `skynet/src/http/` / `skynet/src/proxy/` | ✅ 已完成 / Done |
| 11 | Raft 共识与分片 / Raft & Sharding | 分布式 / Distributed | 6 分布式 / Distributed | `distributed/` (planned) / `services/meta/watcher.go` | 🚧 进行中 / In progress |
| 12 | Go 微服务与 Next.js 控制台 / Go µServices & Next.js | 应用 / Application | 7 应用 / Application | `services/` / `gateway/` / `web/` | ✅ 已完成 / Done |
| 13 | 系统设计与面试题汇总 / System Design & Interview Q&A | 面试 / Interview | 8 面试 / Interview | 全项目 / whole project + `tests/course/` | ✅ 已完成 / Done |
| 14 | 从零复现整个项目 / Rebuild the Entire Project | 复现 / Reproduction | 9 复现 / Reproduction | 全项目（端到端） / whole project (end-to-end) | 🚧 进行中 / In progress |

> 完整大纲请进入对应语言版本 / For the full syllabus, enter the language version of your choice:
> - **中文 / Chinese** → [zh/README.md](./zh/README.md)
> - **English** → [en/README.md](./en/README.md)

---

## 九大学习阶段 / Nine Learning Stages

| 阶段 / Stage | 模块 / Modules | 目标 / Goal |
|---|---|---|
| 1 准备 / Prep | 00-01 | 跑通 Win/Linux/macOS 三套环境，理解整体架构 / Get Win/Linux/macOS environments running; understand overall architecture |
| 2 基础 / Foundation | 02-04 | 补齐 C++/Go/TS 核心语法 / Cover core C++/Go/TS syntax |
| 3 数据结构 / Data Structures | 05-06 | 跳表 + 布隆过滤器，会写会证 / SkipList + BloomFilter: write and prove |
| 4 存储引擎 / Storage Engine | 07-08 | LSM-Tree + Compaction + MVCC | 
| 5 网络 / Networking | 09-10 | epoll + C++20 协程 + HTTP 反向代理 / epoll + C++20 coroutines + HTTP reverse proxy |
| 6 分布式 / Distributed | 11 | Raft 共识 + 一致性哈希分片 / Raft consensus + consistent-hash sharding |
| 7 应用 / Application | 12 | Go 微服务 + Next.js 控制台 / Go µServices + Next.js console |
| 8 面试 / Interview | 13 | 110+ 真题 + 系统设计 + 手撕专题 / 110+ questions + system design + hand-write series |
| 9 复现 / Reproduction | 14 | 从空目录复现整个 TitanKV / Rebuild the entire TitanKV from an empty directory |

---

## 课程特色 / Course Highlights

**中文 / Chinese**

- **背景驱动**：每个模块从「为什么」开始，不背 API。
- **逐层递进**：基础 → 数据结构 → 存储 → 网络 → 分布式 → 应用 → 面试 → 复现，每一层只依赖上一层。
- **源码对照**：所有概念都对应 TitanKV 真实代码（`minikv/` / `skynet/` / `services/` / `web/`）。
- **中英双语**：每个模块都有 zh + en 两版，技术名词保留英文原词，方便简历与面试直接使用。
- **手撕代码**：跳表 / LRU / 线程池 / 智能指针 / epoll 服务器，全部对应 `tests/course/` 下的真实单测。
- **可运行**：所有代码可在 Win/Linux/macOS 上本地编译运行，并提供 Docker Compose 一键启动本地开发栈。
- **求职导向**：Module 13 汇总 50+ 真实面试题（含 LeetCode 题号与手撕题骨架），直接服务求职冲刺。

**English**

- **Motivation first**: Every module starts with "why," not "here's the API."
- **Layer-by-layer progression**: Foundation → Data Structures → Storage → Networking → Distributed → Application → Interview → Reproduction. Each layer only depends on the previous one.
- **Source-bound**: Every concept maps to real TitanKV code (`minikv/` / `skynet/` / `services/` / `web/`).
- **Bilingual**: Every module has both `zh` and `en` versions, with technical terms kept in their original English form so you can use them directly on a résumé and in interviews.
- **Hand-write series**: SkipList / LRU / ThreadPool / SmartPtr / epoll server — all backed by real unit tests under `tests/course/`.
- **Runnable**: All code builds and runs on Win/Linux/macOS, with a one-line Docker Compose to bring up the local dev stack.
- **Interview-oriented**: Module 13 condenses 50+ real interview questions (with LeetCode numbers and hand-write skeletons) — directly serving your job-search crunch.

---

## 如何使用 / How to Use

1. 按模块顺序学习，每个模块先读「核心知识」，再动手完成「动手题」。
   Learn modules in order. Read "Core Knowledge" first, then complete the "Hands-on Exercises."
2. 动手题对应 TitanKV 真实源码，建议对照阅读并在本地编译运行。
   Hands-on exercises map to real TitanKV source. Read alongside and build/run locally.
3. 「自检」用于检验掌握程度，先独立作答再对照参考答案。
   "Self-Check" verifies mastery. Answer independently first, then compare with the reference answer.
4. 模块 13 汇总 110+ 道真实面试题（含 LeetCode 题号、一面/二面/三面索引与手撕题），用于求职冲刺。
   Module 13 condenses 110+ real interview questions (with LeetCode numbers, round-1/2/3 index, and hand-write problems) for job-search crunch.
5. 模块 14 要求从空目录复现整个项目，建议边学边在另一个仓库复现当周内容。
   Module 14 asks you to rebuild the entire project from an empty directory. We suggest reproducing each week's content in another repo as you learn.

---

## 目录结构 / Layout

```
docs/course/
├── README.md            本文件（双语入口）/ this file (bilingual entry)
├── zh/                  中文文档 / Chinese docs
│   ├── README.md        中文大纲 / Chinese syllabus
│   ├── 00-cross-platform-env.md   （规划中 / planned）
│   ├── 01-overview.md … 13-interview.md
│   └── 14-reproduce.md
└── en/                  English docs
    ├── README.md        English syllabus
    ├── 00-cross-platform-env.md   (planned)
    ├── 01-overview.md … 13-interview.md
    └── 14-reproduce.md
```

---

## 快速开始 / Quick Start

```bash
# 克隆仓库 / Clone
git clone <repo-url> titan-kv
cd titan-kv

# C++ 构建与测试 / C++ build & test
cmake -B build -DCMAKE_BUILD_TYPE=Release -DENABLE_TESTS=ON
cmake --build build -j
ctest --test-dir build --output-on-failure

# 本地开发栈 + Go 服务 + 控制台 / Dev stack + Go services + console
docker compose -f deploy/dev/docker-compose.yml up -d
make run-all
make web-install && make web-dev   # http://localhost:3000
```

---

## 下一步 / Next Step

- **中文 / Chinese**：进入 [Module 00 — 跨平台环境搭建](./zh/00-cross-platform-env.md) 开始学习。
- **English**: Proceed to [Module 00 — Cross-Platform Env Setup](./en/00-cross-platform-env.md) to start learning.

如果你已经在 Linux 上跑通过项目，可以直接跳到 / If you already have the project running on Linux, you can jump directly to:
- [Module 01 — 环境搭建与项目概览](./zh/01-overview.md)
- [Module 01 — Env Setup & Project Overview](./en/01-overview.md)
