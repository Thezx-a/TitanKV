<p align="center">
  <img src="https://img.shields.io/badge/C%2B%2B-17-00599C?style=for-the-badge&logo=cplusplus&logoColor=white" alt="C++17"/>
  <img src="https://img.shields.io/badge/Build-CMake-064F8C?style=for-the-badge&logo=cmake&logoColor=white" alt="CMake"/>
  <img src="https://img.shields.io/badge/Tests-17%20suites-brightgreen?style=for-the-badge" alt="Tests"/>
  <img src="https://img.shields.io/badge/License-MIT-blue?style=for-the-badge" alt="MIT"/>
</p>

<h1 align="center">🗄️ MiniKV</h1>

<p align="center">
  <b>Embedded LSM-Tree Key-Value Storage Engine</b><br/>
  <i>WAL · Bloom Filter · SSTable · Compaction — built from scratch in C++17</i>
</p>

---

## Overview

MiniKV is a **from-scratch implementation** of the core architecture behind LevelDB and RocksDB — the LSM-Tree (Log-Structured Merge-Tree). Latest engine details: MemTable held by `shared_ptr` (Get/Iterator after lock snapshot), BlockCache for decompressed SST blocks, and a master/sub Reactor TCP server (epoll LT).

### Write Path

```mermaid
sequenceDiagram
    participant Client
    participant DB as DB
    participant WAL as WAL (Disk)
    participant Mem as MemTable
    participant SST as SSTable (Disk)

    Client->>DB: Put(key, value)
    DB->>WAL: Append record
    WAL-->>DB: ACK (durable)
    DB->>Mem: Insert into SkipList
    Mem-->>DB: OK

    alt MemTable full
        DB->>Mem: Create new MemTable
        DB->>SST: Flush old MemTable to L0
        SST-->>DB: SSTable file written
    end

    DB-->>Client: OK

    Note over DB,SST: Compaction runs in background
```

### Read Path

```mermaid
sequenceDiagram
    participant Client
    participant DB as DB
    participant Mem as MemTable
    participant BF as Bloom Filter
    participant SST as SSTables

    Client->>DB: Get(key)
    DB->>Mem: Check MemTable
    
    alt Found in MemTable
        Mem-->>DB: value
    else Not found
        DB->>SST: Check L0 SSTables
        loop For each SSTable
            DB->>BF: MightContain(key)?
            alt BF says "definitely not"
                BF-->>DB: Skip this SSTable
            else BF says "maybe"
                DB->>SST: Binary search in SSTable
                SST-->>DB: value or not found
            end
        end
    end

    DB-->>Client: value or NotFound
```

### LSM-Tree Architecture

```mermaid
graph TB
    subgraph "Memory"
        WT[Write Thread] --> ML[MemTable<br/>SkipList]
    end

    subgraph "Disk"
        ML -->|flush| L0[L0 SSTables<br/>overlapping]
        L0 -->|compact| L1[L1 SSTables<br/>non-overlapping]
        L1 -->|compact| L2[L2 SSTables<br/>larger]
    end

    subgraph "WAL"
        WT --> WAL[Write-Ahead Log<br/>append-only]
    end

    style ML fill:#e1f5fe
    style L0 fill:#f3e5f5
    style L1 fill:#e8f5e9
    style L2 fill:#fff3e0
    style WAL fill:#fce4ec
```

---

## Core Concepts

| Component | What It Does |
|-----------|-------------|
| **MemTable** | In-memory sorted buffer (SkipList). Writes go here first. |
| **WAL** | Write-Ahead Log. Guarantees durability on crash. |
| **SSTable** | Sorted String Table. Immutable on-disk sorted files. |
| **Bloom Filter** | Probabilistic structure: "definitely not in this SSTable" |
| **Compaction** | Merges SSTables to reclaim space and reduce read levels. |
| **BlockCache** | LRU of decompressed SST data blocks (`block_cache.h`). |
| **Skip List** | Probabilistic sorted structure used as MemTable backend. |

---

## Quick Start

```bash
# Clone
git clone https://github.com/Thezx-a/MiniKV.git
cd MiniKV

# Build & Test
cmake -B build -G Ninja -DCMAKE_BUILD_TYPE=Release \
  -DENABLE_TESTS=ON -DCMAKE_CXX_COMPILER=g++-12
cmake --build build -j$(nproc)
ctest --test-dir build --output-on-failure
```

### Use as a Library

```cpp
#include <minikv/db.h>

minikv::Options opts;
opts.create_if_missing = true;
minikv::DB* db;
minikv::DB::Open(opts, "/tmp/mydb", &db);

db->Put("key", "value");
std::string val;
db->Get("key", &val);  // val == "value"

delete db;
```

---

## Project Structure

```
MiniKV/
├── include/minikv/          Public API headers
│   ├── db.h                 DB open/put/get/delete
│   ├── options.h            Configuration
│   ├── comparator.h         Key comparison
│   └── write_batch.h        Atomic batch writes
├── src/
│   ├── db/                  DB implementation
│   ├── core/                LSM-Tree internals
│   │   ├── skiplist.h       MemTable backend
│   │   ├── wal.h            Write-Ahead Log
│   │   ├── sstable.h        SSTable reader/writer
│   │   ├── compaction.h     Merge logic
│   │   └── bloom.h          Bloom filter
│   ├── network/             Main/sub Reactor (epoll LT)
│   │   ├── event_loop.h     epoll + eventfd runInLoop
│   │   ├── event_loop_thread.h  Sub Reactor 线程池
│   │   ├── server.h         accept 在 Main，连接在 Sub
│   │   └── connection.h     粘包缓冲
│   ├── table/               Table operations
│   └── utils/               Utilities
│       ├── coding.h         Varint encoding
│       ├── crc32.h          Checksums
│       ├── hash.h           Hash functions
│       ├── block_cache.h    SST decompressed block LRU (engine)
│       ├── lru_cache.h      Generic LRU template (utils)
│       └── thread_pool.h    Async compaction
├── tests/                   GoogleTest suites (see CMakeLists)
├── benches/                 Benchmarks
├── client/                  Python asyncio client
├── docker/                  Docker deployment
└── docs/                    Design documentation
```

---

## Bloom Filter Efficiency

```mermaid
pie title FP Rate by Bits/Key (k=7)
    "4 bits/key: 3.1% FP" : 3.1
    "8 bits/key: 0.8% FP" : 0.8
    "12 bits/key: 0.2% FP" : 0.2
    "16 bits/key: 0.05% FP" : 0.05
    "Effective (99.95%)" : 99.95
```

| Bits/Key | FP Rate | Memory per 1M keys |
|----------|---------|-------------------|
| 4 | 3.1% | 500 KB |
| 8 | 0.8% | 1 MB |
| 12 | 0.2% | 1.5 MB |
| 16 | 0.05% | 2 MB |

---

## Tests

```mermaid
graph LR
    subgraph "22 Unit Tests"
        A[SkipList: 3]
        B[BloomFilter: 3]
        C[WAL: 3]
        D[SSTable: 2]
        E[Compaction: 2]
        F[DB: 5]
        G[ThreadPool: 2]
        H[Coding: 2]
    end

    A -->|backend| MEM[MemTable]
    B -->|filter| SST[SSTable]
    C -->|durable| WAL[Write Path]
    D -->|storage| L0[L0 SSTables]
    E -->|merge| L1[L1 SSTables]
    F -->|integration| DB[Full DB]

    style A fill:#e1f5fe
    style F fill:#e8f5e9
```

| Module | Tests | What's Verified |
|--------|-------|-----------------|
| SkipList | 3 | Insert, iterator, memory accounting |
| BloomFilter | 3 | FP rate, serialization, integration |
| WAL | 3 | Append, recovery, truncate |
| SSTable | 2 | Build, lookup |
| Compaction | 2 | L0→L1 merge, tombstone removal |
| DB | 5 | Put/Get, delete, batch, reopen, crash recovery |
| ThreadPool | 2 | Concurrent tasks, shutdown |
| Coding | 2 | Varint, fixed32 |

---

## Tech Stack

`C++17` `CMake` `Ninja` `Epoll` `Google Test` `Python asyncio`

---

## License

[MIT](LICENSE)
