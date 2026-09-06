#!/usr/bin/env bash
# =========================================================
# TitanKV RAG Router E2E (M2)
# 链路: curl → rag-service(:18086) → minikv_server(:18889, 临时DB)
# 覆盖: L1 wiki 直答路由 / L2 单发 / L3 降级 / rag_route_total 指标
# 前提: WikiLLM=false (规则 summary compile, 无需 LLM 后端)
# 用法: bash scripts/e2e_rag_router.sh
# =========================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MINIKV_BIN="${ROOT}/build/minikv/minikv_server"
GO="${GO:-$(command -v go || echo /usr/local/go/bin/go)}"
DB="$(mktemp -d)"
IDX="$(mktemp -d)"
MINIKV="127.0.0.1:18889"
RAG="127.0.0.1:18086"
MINIKV_PID=""; RAG_PID=""

cleanup() {
  [ -n "$RAG_PID" ] && kill "$RAG_PID" 2>/dev/null || true
  [ -n "$MINIKV_PID" ] && kill "$MINIKV_PID" 2>/dev/null || true
  rm -rf "$DB" "$IDX"
}
trap cleanup EXIT

fail() { echo "[E2E-FAIL] $*" >&2; exit 1; }
step() { echo; echo "===== $* ====="; }

step "start minikv + rag (RAG_ENABLE_ROUTER=true, WikiLLM=false)"
"$MINIKV_BIN" --host 127.0.0.1 --port 18889 --db "$DB" >/tmp/e2e-router-minikv.log 2>&1 &
MINIKV_PID=$!
sleep 1
( cd "$ROOT" && RAG_ADDR="$RAG" MINIKV_ADDR="$MINIKV" RAG_INDEX_DIR="$IDX" \
  RAG_ASYNC_INGEST=false RAG_ENABLE_BM25=true RAG_ENABLE_WIKI=true \
  RAG_WIKI_LLM=false RAG_ENABLE_ROUTER=true \
  "$GO" run ./services/rag/cmd >/tmp/e2e-router-rag.log 2>&1 ) &
RAG_PID=$!
for i in $(seq 1 30); do curl -sf "http://$RAG/healthz" >/dev/null 2>&1 && break; sleep 1; done
curl -sf "http://$RAG/healthz" | python3 -c 'import json,sys; h=json.load(sys.stdin); assert h["router"] is True and h["wiki"] is True, h' \
  || fail "healthz router/wiki flags"

step "1) ingest markdown doc (heading → wiki 实体页)"
curl -sf -X POST "http://$RAG/api/rag/collections/e2e/documents" \
  -H 'Content-Type: application/json' -d '{
    "title":"storage-engines",
    "text":"# LSM Tree\n\nLSM-Tree 将写入先落在内存 MemTable, 再批量刷成 SSTable, 用顺序写替换随机写。\n\n# Bloom Filter\n\n布隆过滤器用位数组和多个哈希函数快速判断键可能存在, 空间开销极小。"
  }' | grep -q '"status":"success"' || fail "ingest"

step "2) compile wiki (规则模式) 并等待 task success"
task=$(curl -sf -X POST "http://$RAG/api/rag/collections/e2e/wiki/compile" \
  -H 'Content-Type: application/json' -d '{"doc_id":""}' | python3 -c '
import json,sys
r=json.load(sys.stdin)
t=(r.get("task_ids") or [r.get("task_id","")])[0]
print(t or "")')
[ -n "$task" ] || fail "no compile task id"
for i in $(seq 1 20); do
  st=$(curl -sf "http://$RAG/api/rag/collections/e2e/wiki/tasks/$task" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')
  [ "$st" = "success" ] && break
  [ "$st" = "failed" ] && fail "compile failed"
  sleep 0.5
done
[ "${st:-}" = "success" ] || fail "compile task not success: ${st:-timeout}"
curl -sf "http://$RAG/api/rag/collections/e2e/wiki/index" | grep -q 'LSM Tree' || fail "wiki index missing LSM Tree page"

step "3) L1: 查询命中 wiki 页标题 → route=L1_wiki 且带 wiki 字段"
resp=$(curl -sf -X POST "http://$RAG/api/rag/collections/e2e/retrieve" \
  -H 'Content-Type: application/json' -d '{"query":"LSM Tree","top_k":3}')
echo "$resp" | python3 -c '
import json,sys
r=json.load(sys.stdin)
assert r["route"]["path"]=="L1_wiki", r["route"]
assert r["route"]["reason"].startswith("wiki_title_match") or r["route"]["reason"].startswith("wiki_exact_slug"), r["route"]
assert r.get("wiki_count",0)>0 and r["wiki"][0]["slug"].startswith("lsm"), r.get("wiki")
print("route:", r["route"]["path"], "| wiki:", r["wiki"][0]["slug"])' || fail "L1 route: $resp"

step "4) L2: 普通事实查询 → route=L2_single"
resp=$(curl -sf -X POST "http://$RAG/api/rag/collections/e2e/retrieve" \
  -H 'Content-Type: application/json' -d '{"query":"WAL 是什么","top_k":3}')
echo "$resp" | python3 -c '
import json,sys
r=json.load(sys.stdin)
assert r["route"]["path"]=="L2_single", r["route"]
assert "wiki" not in r
print("route:", r["route"]["path"], "| reason:", r["route"]["reason"])' || fail "L2 route: $resp"

step "5) L3: 对比型查询 → route=L3_degraded (agentic pending M3)"
resp=$(curl -sf -X POST "http://$RAG/api/rag/collections/e2e/retrieve" \
  -H 'Content-Type: application/json' -d '{"query":"LSM Tree 和 Bloom Filter 的区别","top_k":3}')
echo "$resp" | python3 -c '
import json,sys
r=json.load(sys.stdin)
assert r["route"]["path"]=="L3_degraded", r["route"]
assert "agentic_pending_m3" in r["route"]["reason"], r["route"]
assert r["count"]>0
print("route:", r["route"]["path"], "| reason:", r["route"]["reason"])' || fail "L3 route: $resp"

step "6) /metrics 暴露 rag_route_total 三路计数"
metrics=$(curl -sf "http://$RAG/metrics")
echo "$metrics" | grep -q 'rag_route_total{path="L1_wiki"}' || fail "metric L1 missing"
echo "$metrics" | grep -q 'rag_route_total{path="L2_single"}' || fail "metric L2 missing"
echo "$metrics" | grep -q 'rag_route_total{path="L3_degraded"}' || fail "metric L3_degraded missing"
echo "$metrics" | grep 'rag_route_total' | head -3

echo
echo "[E2E-PASS] Router 端到端 6 步全部通过 (L1 直答 / L2 单发 / L3 降级 / metrics)"
