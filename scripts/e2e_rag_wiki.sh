#!/usr/bin/env bash
# =========================================================
# TitanKV Wiki 版本回滚 + 审计 E2E (M5)
# 链路: curl → rag-service(:18087) → minikv_server(:18890, 临时DB)
# 覆盖: compile 两版 → rollback 恢复旧版 → /wiki/audit 支撑度报告
# 用法: bash scripts/e2e_rag_wiki.sh
# =========================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MINIKV_BIN="${ROOT}/build/minikv/minikv_server"
GO="${GO:-$(command -v go || echo /usr/local/go/bin/go)}"
DB="$(mktemp -d)"; IDX="$(mktemp -d)"
MINIKV="127.0.0.1:18890"; RAG="127.0.0.1:18087"
MINIKV_PID=""; RAG_PID=""

cleanup() { [ -n "$RAG_PID" ] && kill "$RAG_PID" 2>/dev/null || true; [ -n "$MINIKV_PID" ] && kill "$MINIKV_PID" 2>/dev/null || true; rm -rf "$DB" "$IDX"; }
trap cleanup EXIT
fail() { echo "[E2E-FAIL] $*" >&2; exit 1; }
step() { echo; echo "===== $* ====="; }

step "start minikv + rag"
"$MINIKV_BIN" --host 127.0.0.1 --port 18890 --db "$DB" >/tmp/e2e-wiki-minikv.log 2>&1 &
MINIKV_PID=$!
sleep 1
( cd "$ROOT" && RAG_ADDR="$RAG" MINIKV_ADDR="$MINIKV" RAG_INDEX_DIR="$IDX" \
  RAG_ASYNC_INGEST=false RAG_ENABLE_WIKI=true RAG_WIKI_LLM=false RAG_WIKI_KEEP_VERSIONS=3 \
  "$GO" run ./services/rag/cmd >/tmp/e2e-wiki-rag.log 2>&1 ) &
RAG_PID=$!
for i in $(seq 1 30); do curl -sf "http://$RAG/healthz" >/dev/null 2>&1 && break; sleep 1; done

step "1) ingest + compile v1"
curl -sf -X POST "http://$RAG/api/rag/collections/e2e/documents" \
  -H 'Content-Type: application/json' -d '{
    "title":"lsm-doc",
    "text":"# LSM Tree\n\nLSM-Tree 写入先进 MemTable 内存跳表, 达到阈值后不可变刷成 SSTable 落盘, 后台 Compaction 合并多层。"
  }' | grep -q '"status":"success"' || fail "ingest"
task=$(curl -sf -X POST "http://$RAG/api/rag/collections/e2e/wiki/compile" \
  -H 'Content-Type: application/json' -d '{"doc_id":""}' | python3 -c 'import json,sys; r=json.load(sys.stdin); print((r.get("task_ids") or [r.get("task_id","")])[0])')
for i in $(seq 1 20); do
  st=$(curl -sf "http://$RAG/api/rag/collections/e2e/wiki/tasks/$task" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",""))')
  [ "$st" = "success" ] && break; sleep 0.5
done
[ "${st:-}" = "success" ] || fail "compile v1"
v1=$(curl -sf "http://$RAG/api/rag/collections/e2e/wiki/pages/lsm-tree" | python3 -c 'import json,sys; print(json.load(sys.stdin)["body"])')
echo "v1 body: ${v1:0:40}..."

step "2) 篡改: 通过 minikv 原生协议直写幻觉页 (模拟坏 compile)"
python3 - <<'PYEOF'
import socket, struct, json, sys
MAGIC, CMD_PUT = 0x4D4B, 1
s = socket.create_connection(("127.0.0.1", 18890), timeout=5)
page = {"frontmatter": {"title": "LSM Tree", "slug": "lsm-tree", "type": "entity",
        "sources": ["__doc__"], "updated_at": 1788700000},
        "body": "被幻觉污染的内容: LSM-Tree 使用 Raft 协议跨三个可用区同步副本, 并由 Kafka 消费写请求。",
        "summary": "幻觉摘要"}
b = json.dumps(page, ensure_ascii=False).encode()
key = b"wiki:page:e2e:lsm-tree"
req = struct.pack("<HBII", MAGIC, CMD_PUT, len(key), len(b)) + key + b
s.sendall(req)
hdr = s.recv(7)
assert hdr[0:2] == struct.pack("<H", MAGIC) and hdr[2] == 0, hdr
print("tampered page written")
s.close()
PYEOF
bad=$(curl -sf "http://$RAG/api/rag/collections/e2e/wiki/pages/lsm-tree" | python3 -c 'import json,sys; b=json.load(sys.stdin)["body"]; assert "Raft" in b, b; print("bad-page-ok")') || fail "tamper write"
echo "$bad"

step "3) rollback → 恢复上一版"
curl -sf -X POST "http://$RAG/api/rag/collections/e2e/wiki/pages/lsm-tree/rollback" \
  -H 'Content-Type: application/json' -d '{"version":0}' \
  | python3 -c 'import json,sys; r=json.load(sys.stdin); assert r["ok"], r' || fail "rollback call"
rb=$(curl -sf "http://$RAG/api/rag/collections/e2e/wiki/pages/lsm-tree" | python3 -c 'import json,sys; print(json.load(sys.stdin)["body"])')
echo "restored: ${rb:0:40}..."
[ "$rb" = "$v1" ] || fail "restored body != v1"
echo "content restored: matches v1 (Raft 幻觉已清除)"

step "4) /wiki/audit 引用支撑度报告"
curl -sf "http://$RAG/api/rag/collections/e2e/wiki/audit" | python3 -c '
import json,sys
r=json.load(sys.stdin)
assert r["pages"]>=1, r
assert r["audited"]>=1, r
assert r["avg_faithfulness"]>0.6, r
print("pages:", r["pages"], "| audited:", r["audited"], "| avg_faithfulness:", round(r["avg_faithfulness"],3), "| below:", r["below_threshold"])' || fail "audit"

echo
echo "[E2E-PASS] Wiki 版本回滚 + 审计 E2E 全部通过 (v1→v2→rollback→audit)"
