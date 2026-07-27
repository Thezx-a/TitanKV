# Module 14 — TitanKV 核心积木冒烟测试

本练习对应课程 [Module 14 — 从零复现整个项目](../../docs/course/zh/14-reproduce.md)。

## 目标

在完整复现 Gateway + 微服务 + 控制台之前，用约 200 行 C++ 验证 Module 05–08 的核心组件可以组合运行：

- **SkipList**：MemTable 有序结构
- **BloomFilter**：点查负向过滤
- **InternalKey**：user_key + sequence + type（MVCC 风格）

## 编译与运行

```bash
cmake -B build -S . -DCMAKE_BUILD_TYPE=Release
cmake --build build -j
./build/module14_reproduce
```

Windows（MSVC）已启用 `/utf-8`；程序 stdout 使用英文，避免控制台编码乱码。

## 与完整复现的关系

| 层级 | 本练习 | 完整 TitanKV |
|------|--------|--------------|
| 存储 | MiniKV 内存 | minikv LSM-Tree |
| 网络 | — | skynet + Gateway |
| 服务 | — | Go 微服务 × 5 |
| 前端 | — | Next.js 控制台 |

完整端到端步骤见 `docs/course/zh/14-reproduce.md`。
