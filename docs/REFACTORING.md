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

- [ ] Gin gateway, middleware chain (RequestID / Logger / Recover / RateLimit / Auth / RBAC)
- [ ] Auth service (register/login/refresh, bcrypt, JWT, RBAC)
- [ ] API Key issuance / revocation + Redis-backed middleware
- [ ] Rate limiter (token bucket via Redis Lua)
- [ ] GitHub OAuth2 login

## Phase 4 — Go data / meta / observability + Go SDK

- [ ] Data service (Put/Get/Delete/Batch/Scan SSE)
- [ ] Meta service (Collection CRUD, hot config via etcd watch)
- [ ] Observability service (metrics aggregation, health-rollup)
- [ ] Go SDK `client-go` with typed errors and retries

## Phase 5 — Distributed: hashicorp/raft (1-node teaching) + sharding later

- [x] `distributed/` — 1-node raft bootstrap, FSM Apply Put/Del, Snapshot/Restore JSON
- [x] Unit test elects leader + Put/Get (`go test ./distributed/...`)
- [x] Optional `MINIKV_ADDR` forward after Apply (best-effort; Raft truth is in-memory FSM)
- [ ] Multi-node join/leave + real quorum
- [ ] etcd service discovery
- [ ] Consistent-hash sharding / rebalance
- [ ] Failover test across 3 nodes

## Phase 6 — Next.js admin console

- [ ] App structure (App Router, Tailwind, Shadcn UI, TanStack Query)
- [ ] Login + route guard
- [ ] Dashboard (QPS, latency, storage, node status; live via SSE)
- [ ] Data Explorer (browse KV, Scan, inline edit, bulk delete)
- [ ] Collection management
- [ ] Users & roles
- [ ] API Key management
- [ ] Config management
- [ ] Cluster status (Raft topology + log entries)

## Phase 7 — Observability + K8s + CI/CD

- [ ] Prometheus metrics from all services
- [ ] OpenTelemetry tracing across Go + C++
- [ ] Loki / Promtail structured logs
- [ ] Helm Chart (StatefulSet for engine + Raft; Deployment for stateless Gos)
- [ ] GitHub Actions multi-stage CI
- [ ] ArgoCD GitOps (optional)

## Phase 8 — CLI + multi-language SDK + docs

- [ ] `client-cli` Cobra tool (keyforge get/put/scan/cluster/members/admin)
- [ ] TypeScript SDK generated from OpenAPI
- [ ] Python SDK generated from OpenAPI
- [ ] Documentation (ARCHITECTURE, STORAGE_ENGINE, DISTRIBUTED, API, DEPLOYMENT)

## Cross-phase utilities

- [ ] Unified Makefile target (build/test/lint/proto/docker-up)
- [ ] Benchmark regression tracking
- [x] Repo-rename: GitHub repo `LumenDB` → `TitanKV`