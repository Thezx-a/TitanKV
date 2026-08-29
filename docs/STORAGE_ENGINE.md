# TitanKV Storage Engine — Design Notes

This document describes the C++ storage engine that powers TitanKV, including
the LSM-Tree layout, on-disk file formats, concurrency model, and durability.

> **Status (latest, 2026-08-29):** Phase 1 core + MVCC InternalKey pipeline (WP 1.2.2 A/B),
> Manifest persistence (WP 1.2.4), SSTable block compression (WP 1.2.1),
> **BlockCache** + E3 hit/miss metrics (`titankv_engine_block_cache_*`),
> **MemTable `shared_ptr`** (Get/Iterator snapshot under lock, then lock-free),
> **Range Delete** E5 (`DB::deleteRange` batched point tombstones ≤10000; not RocksDB range tombstone),
> Compaction L1→Ln + E4 failure retry/backoff + `compaction_failures` metrics,
> and **master/sub Reactor** TCP server (epoll LT, `--io-threads` / `--biz-threads`).
> Still pending: column families (CF), OCC transactions, configurable compaction strategy.

---

## 1. Component Map

```
minikv/
├── include/minikv/         Public headers (DB / Options / Slice / Iterator)
├── src/core/               LSM-Tree core
│   ├── db_impl.{h,cpp}     DB orchestrator (group commit + flush queue)
│   ├── memtable.{h,cpp}    SkipList-backed in-memory table
│   ├── skip_list.h         Concurrent skip list (shared_mutex)
│   ├── wal.{h,cpp}         Per-generation write-ahead log (wal-<N>.log)
│   ├── block.{h,cpp}       Sorted data block (prefix sharing)
│   ├── block_cache.h       Byte-capacity LRU for decompressed SST blocks
│   ├── bloom_filter.h      Per-SST bloom (MurmurHash2)
│   ├── sstable_builder.{h,cpp}
│   ├── sstable_reader.{h,cpp}   Uses BlockCache when provided
│   ├── sstable_iterator / memtable_iterator / merging_iterator
│   ├── compaction.{h,cpp}  Background L0→L1+ merge
│   ├── version.{h,cpp}     In-memory LSM topology
│   ├── manifest.{h,cpp}    Durable Version edits
│   ├── compression.{h,cpp} Snappy / Zstd
│   └── internal_key.{h,cpp} [user_key | seq | type] codec
└── src/network/            Native TCP server (master/sub Reactor, epoll LT)
    ├── server.{h,cpp}      accept on Main; IO on Sub; biz pool optional
    ├── event_loop*.{h,cpp} epoll + eventfd runInLoop
    └── connection.{h,cpp}  sticky-packet buffer + in_flight_ serialisation
```

---

## 2. LSM-Tree Shape

```
        Write ─────► active MemTable (shared_ptr<MemTable>, SkipList)
                            │ rotate when full
                            ▼
                      immutable_list_  (still readable by Get/Iterator)
                            │ flush thread (flushOne, no write lock during IO)
                            ▼
                      L0 SSTables (overlapping) ──compaction──► L1 … L7
```

Default `Options` (see `include/minikv/options.h`):

| Field | Default | Notes |
|-------|---------|-------|
| `memtable_size` | 4 MB | flush trigger |
| `block_size` | 4 KB | SST data block |
| `lru_cache_capacity` | 8 MB | BlockCache capacity |
| `max_level` | 7 | L0 .. L7 |
| `level0_compaction_trigger` | 4 | L0 file count |
| `wal_sync` | true (CLI may override) | fdatasync policy |
| `compression` | 1 (snappy) | per-block |

---

## 3. Concurrency: MemTable `shared_ptr` + BlockCache

### 3.1 MemTable ownership

- `memtable_` and each entry in `immutable_list_` are `std::shared_ptr<MemTable>`.
- **Get / newIterator**: under `write_mutex_`, copy `shared_ptr` snapshots of active + immutables, then **release the lock** and look up lock-free while refcounts keep tables alive.
- **Flush**: flush thread holds the same `shared_ptr` via `flush_queue_`; heavy SST IO runs **without** `write_mutex_`; only erasing from `immutable_list_` re-acquires the lock briefly.
- Writers still serialise on `write_mutex_` (group commit / WAL / rotate).

### 3.2 BlockCache

- Key: `(sst_path, block_offset)`; value: decompressed block bytes.
- Capacity: `Options::lru_cache_capacity` bytes; LRU eviction under mutex.
- `SSTableReader::open(path, block_cache)` shares the cache; compaction/deletes call `invalidatePath`.

### 3.3 Network (minikv_server)

- Main Reactor: accept only.
- Sub Reactors: `--io-threads` (default **4**), epoll **LT**, `EPOLLIN` only.
- Biz pool: `--biz-threads` (default **4**); `processRequest` off IO thread; response `queueInLoop` back to owning Sub.
- `--io-threads 0` / `--biz-threads 0` collapse to single-thread / sync-on-Sub modes.

---

## 4. On-disk File Formats

### 4.1 SSTable

Data blocks (13-byte compressed header) + Index + Footer(48B, magic `MKSSTABL`).
See WP 1.2.1 notes in git history / `compression.cpp`.

### 4.2 Bloom sidecar

`<sst>.bloom` — consulted before block read.

### 4.3 WAL

Per MemTable generation: `wal-<N>.log`, records `[crc][len][payload]`.
Rotate on flush; replay all residual WALs on open.

### 4.4 MANIFEST

Append-only Version edits (`kReset` / `kAdd` / `kDel`); torn tail discarded.

---

## 5. Internal Key (WP 1.2.2)

```
internal_key = user_key || trailer(8 LE)
trailer      = (seq << 8) | ValueType
```

Compare: user_key ASC, seq DESC, type ASC. Used end-to-end in MemTable / SST / iterators.

---

## 6. Write Path

```
put/write → group commit (optional batching) → WAL append → MemTable insert
         → maybeFlush: rotate memtable_ into immutable_list_ + flush_queue_
         → flush thread: build L0 SST → Version/MANIFEST → drop WAL files
         → CompactionManager may compact L0→L1+
```

---

## 7. Read Path

```
get → snapshot shared_ptr MemTables (lock) → search active+immutables (lock-free)
    → for each SST: Bloom → index → BlockCache miss? read+decompress+cache → block get
```

---

## 8. Durability Model

| Event | Effect |
|-------|--------|
| Crash before WAL sync | last batch may be lost |
| Crash after WAL, before SST | WAL replay rebuilds MemTable |
| Crash mid-SST | MANIFEST never listed partial file → ignored |
| Crash mid-MANIFEST append | bad CRC tail discarded |

---

## 9. Pending Work

| WP | Topic |
|----|-------|
| 1.2.3 | Range Delete |
| 1.2.5 | Column Family |
| 1.2.6 | OCC transactions |
| 1.2.7 | Compaction strategy switch |

See `docs/REFACTORING.md`.
