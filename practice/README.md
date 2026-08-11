# TitanKV 课程练习代码（practice）

位于 `/root/practice`（与 hellocpp 同级），按课程 Module 00–14 存放可独立编译运行的练习。

仓库内合并版：`/root/hellocpp/practice`（含 `tests/course` 手撕 `handwrite/`）。本目录仅为 Module 00–14，不含 handwrite。

每章均包含详细中文注释源码与独立 `CMakeLists.txt`，并支持在本章目录下单独生成 `build/`。

## 环境要求

- CMake >= 3.20
- GCC >= 12（Module 09 推荐 C++20；当前 WSL GCC 11 亦可编译本练习）
- Linux / WSL2 推荐

## 1. 如何编译单章

以 `module00_env_check` 为例（其他章把目录名换成对应 `moduleXX_*`）：

```bash
cmake -B /root/practice/module00_env_check/build \
      -S /root/practice/module00_env_check \
      -DCMAKE_BUILD_TYPE=Release
cmake --build /root/practice/module00_env_check/build -j$(nproc)
```

也可在章目录内：

```bash
cd /root/practice/module00_env_check
cmake -B build -S . -DCMAKE_BUILD_TYPE=Release
cmake --build build -j$(nproc)
```

## 2. 如何运行单章

编译成功后，可执行文件在本章的 `build/` 下，文件名与目录名相同：

```bash
/root/practice/module00_env_check/build/module00_env_check
```

或：

```bash
cd /root/practice/module00_env_check
./build/module00_env_check
```

## 3. 如何一键编译 / 运行全部章节

```bash
cd /root/practice
chmod +x build_all_modules.sh run_all_modules.sh   # 首次需要
./build_all_modules.sh    # 每章各自写入 moduleXX_*/build/
./run_all_modules.sh      # 依次运行各章二进制
```

说明：顶层 `cmake -B build` 仍可用（统一构建到 `/root/practice/build`），但推荐按章使用各自 `build/`，与脚本一致。

## 4. 各模块运行命令一览

| 模块 | 运行命令 |
|------|----------|
| module00_env_check | `/root/practice/module00_env_check/build/module00_env_check` |
| module01_overview | `/root/practice/module01_overview/build/module01_overview` |
| module02_cpp_core | `/root/practice/module02_cpp_core/build/module02_cpp_core` |
| module03_modern_cpp | `/root/practice/module03_modern_cpp/build/module03_modern_cpp` |
| module04_go_ts_concepts | `/root/practice/module04_go_ts_concepts/build/module04_go_ts_concepts` |
| module05_skiplist | `/root/practice/module05_skiplist/build/module05_skiplist` |
| module06_bloom_hash | `/root/practice/module06_bloom_hash/build/module06_bloom_hash` |
| module07_lsm_engine | `/root/practice/module07_lsm_engine/build/module07_lsm_engine` |
| module08_compaction_mvcc | `/root/practice/module08_compaction_mvcc/build/module08_compaction_mvcc` |
| module09_epoll_coro | `/root/practice/module09_epoll_coro/build/module09_epoll_coro` |
| module10_http_proxy | `/root/practice/module10_http_proxy/build/module10_http_proxy` |
| module11_raft_sharding | `/root/practice/module11_raft_sharding/build/module11_raft_sharding` |
| module12_microservices_concepts | `/root/practice/module12_microservices_concepts/build/module12_microservices_concepts` |
| module13_interview | `/root/practice/module13_interview/build/module13_interview` |
| module14_reproduce | `/root/practice/module14_reproduce/build/module14_reproduce` |

## 两处 practice 说明

- `/root/practice`：同级副本，仅 Module 00–14
- `/root/hellocpp/practice`：仓库内合并版，含 handwrite（手撕题请在 `handwrite/` 目录单独按该目录说明编译；本脚本只处理 `module00`–`module14`）