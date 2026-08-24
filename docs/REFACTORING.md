# =========================================================
# TitanKV — top-level refactor plan (track here)
#
# Each phase is split into work packages (WPs).
# Mark status with: [ ] todo   [~] in-progress   [x] done
# =========================================================

## Phase 0 — Cleanup and restructure

- [x] Remove legacy layers and old project code
- [x] Remove outdated docs (ARCHITECTURE, PRODUCTION_QA, OPERATIONS, etc.)
- [x] Keep `minikv/` — C++17 LSM-Tree storage engine core
- [x] Keep `skynet/` — C++20 coroutine network library
- [x] Add top-level skeleton: `gateway/ services/ distributed/ client-go/ client-cli/ web/ proto/ deploy/ docs/`
- [x] New `.gitignore`, `go.mod`, top-level `CMakeLists.txt`, `README.md`, `Makefile`
- [x] `deploy/dev/docker-compose.yml` — local dev stack (Postgres, Redis, etcd, Jaeger, Prometheus, Grafana)

## Phase 1 — C++ storage engine upgrade

- [x] WP 1.2.1  SSTable block compression (Snappy / ZSTD)
      - Added `core/compression.{h,cpp}` with `compressBlock` / `decompressBlock`
      - Added `cmake/FetchCompression.cmake` pulling snappy 1.2.1 + zstd 1.5.6 via FetchContent
      - New on-disk block format: `[crc(4)][physical_size(4)][uncompressed_size(4)][type(1)][payload]`
      - Footer gains 1-byte `format_version` (currently 1)
      - `Options::compression` (uint8_t, default 1=snappy) propagated through `DBImpl::flushMemTable`
      - Tests: `test_compression.cpp` (round-trip, mismatch detection) and `test_sstable_compression.cpp`
      - Status: code-reviewed, awaiting Linux/WSL build verification (Windows native unsupported)
- [x] WP 1.2.2  MVCC snapshot reads (internal key = user_key | seq | type)
      - Phase A: `core/internal_key.{h,cpp}` — encoding helpers + descending-seq
        comparator. Test coverage in `tests/test_internal_key.cpp`.
      - Phase B: Full rewrite of the internal-key pipeline from hash-based uint64_t
        to `[user_key | trailer(8)]` string encoding:
        * `SkipList` now uses `std::string` keys with `InternalKeyCompare`.
        * `MemTableEntry::internal_key` is `std::string`; `put()` calls
          `InternalKeyEncode()`; `get()` scans by user_key prefix.
        * `SSTableBuilder::add()` takes `(internalKey, userKey, value)` — writes
          full internal_key to block, populates bloom with user_key.
        * `SSTableReader::get()` takes user_key, extracts user_key prefix from
          index entries, uses `BlockReader::getByUserKey()` for block scan.
        * `BlockReader::getByUserKey()` — linear scan returning first match on
          user_key (newest version, since seq is descending).
        * `SSTableIterator` / `MemTableIterator` — sort by `InternalKeyCompare`.
        * `MergingIterator` — heap ordering by `InternalKeyCompare`.
        * `DBImpl::flushMemTable()` — passes string internal_keys to builder.
        * Deleted `packInternalKey()` hash function.
      - Tests: `test_skip_list.cpp` (string keys), `test_internal_key.cpp` (codec),
        `test_sstable_compression.cpp` (round-trip + point lookup + MVCC dedup).
- [ ] WP 1.2.3  Range Delete (WriteBatch::deleteRange, MemTable tombstones)
- [x] WP 1.2.4  Manifest persistence (recover Version on restart)
      - Added `core/manifest.{h,cpp}` with appended [crc(4)][size(4)][payload] records.
      - Records cover kReset / kAdd / kDel by level+file_path+file_number.
      - On `DBImpl::open`, Manifest replays to rebuild Version snapshot before WAL replay.
      - `Version` writes through to Manifest on every add/remove; fsync'd both on add and remove.
      - Truncated tail record (CRC fail / short read) is ignored — torn-write tolerant recovery.
      - Tests: 5 cases incl. round-trip, remove-persist, reset-clears, truncated-tail tolerance.
      - Fixed a latent durability gap: prior code restarted with empty Version and lost existing SSTs.
- [x] WP 1.2.8  MemTable `shared_ptr` + lock-free Get/Iterator after snapshot
      - `memtable_` / `immutable_list_` hold `shared_ptr<MemTable>`
      - Get/newIterator copy refs under `write_mutex_`, search outside the lock
      - Flush thread shares ownership via `flush_queue_`; IO without write lock
      - Tests: `test_memtable_snapshot.cpp`
- [x] WP 1.2.9  BlockCache for decompressed SST data blocks
      - `core/block_cache.h` LRU by `(path, offset)` with byte capacity
      - Wired through `SSTableReader::open` / `DBImpl`
      - Tests: `test_block_cache.cpp`
- [ ] WP 1.2.5  Column Family (per-CF MemTable + SST, shared WAL)
- [ ] WP 1.2.6  Optimistic transactions (Begin/Commit/Rollback, OCC)
- [ ] WP 1.2.7  Configurable compaction strategy (Leveled vs Size-tiered)

## Phase 2 — C++ engine ↔ Go Data (TCP MVP; gRPC later)

- [x] Native TCP protocol already in `minikv/src/network/` (Put/Get/Del + Scan)
- [x] Go client `services/data/minikv_client.go` + `MINIKV_ADDR` wiring
- [x] Fixed EventLoop UAF (copy callback before invoke) — connection close SIGSEGV
- [x] Compaction L0→L1 actually merges entries (no longer empty stub)
- [x] Local bench numbers in `minikv/docs/benchmark.md` + `scripts/bench_minikv.sh`
- [x] Smoke: `scripts/e2e_smoke.sh`
- [ ] `proto/` gRPC + cgo embedded mode (optional upgrade path)

## Phase 3 — Go API gateway + auth service

- [x] Gin gateway, middleware chain (RequestID / Logger / Recover / RateLimit / Auth / RBAC)
- [x] Auth service (register/login/refresh, bcrypt, JWT, RBAC)
- [x] API Key issuance / revocation + Redis-backed middleware (dev-friendly defaults)
- [x] Rate limiter (token bucket via Redis Lua when Redis up)
- [ ] GitHub OAuth2 login

## Phase 4 — Go data / meta / observability + Go SDK

- [x] Data service (Put/Get/Delete/Batch/Scan SSE) + `MINIKV_ADDR` TCP client
- [x] Meta service (Collection CRUD; rag_config / type=rag)
- [x] Observability service (metrics aggregation / scraper hooks)
- [x] Go SDK `client-go/titan` (basic)
- [x] RAG service `services/rag` (:8085) — ingest / retrieve / chat SSE + HNSW side index

## Phase 5 — Distributed: hashicorp/raft (1-node teaching) + sharding later

- [x] `distributed/` — 1-node raft bootstrap, FSM Apply Put/Del, Snapshot/Restore JSON
- [x] Unit test elects leader + Put/Get (`go test ./distributed/...`)
- [x] Optional `MINIKV_ADDR` forward after Apply (best-effort; Raft truth is in-memory FSM)
- [x] `cluster.go` JoinCluster helper + `cmd/raft` entry + `gateway/shard.go` helper
- [ ] Multi-node join/leave + real quorum
- [ ] etcd service discovery
- [ ] Consistent-hash sharding / rebalance
- [ ] Failover test across 3 nodes

## Phase 6 — Next.js admin console

- [x] App structure (App Router, Tailwind, TanStack Query)
- [x] Login + route guard
- [x] Dashboard live metrics (SSE)
- [x] Data Explorer (browse KV / Scan)
- [x] Collection management (incl. type=rag)
- [x] RAG pages (`web/app/dashboard/rag/*`) + chat SSE UI
- [x] Cluster page (`web/app/dashboard/cluster`)
- [ ] Users & roles / API Key / Config polish

## Phase 7 — Observability + K8s + CI/CD

- [ ] Prometheus metrics from all services
- [ ] OpenTelemetry tracing across Go + C++
- [ ] Loki / Promtail structured logs
- [ ] Helm Chart (StatefulSet for engine + Raft; Deployment for stateless Gos)
- [ ] GitHub Actions multi-stage CI
- [ ] ArgoCD GitOps (optional)

## Phase 8 — CLI + multi-language SDK + docs

- [x] `client-cli` Cobra `keyforge` (ping/put/get/scan) against gateway
- [ ] cluster/members/admin subcommands
- [ ] TypeScript SDK generated from OpenAPI
- [ ] Python SDK generated from OpenAPI
- [x] Docs sync: README / RUN / STORAGE_ENGINE / RAG-ARCHITECTURE / design.md

## Cross-phase utilities

- [x] Unified Makefile target (build/test/lint/docker-up/run-all/run-rag/smoke-skynet)
- [ ] Benchmark regression tracking
- [x] Repo-rename: GitHub repo `LumenDB` → `TitanKV`