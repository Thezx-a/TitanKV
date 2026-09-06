#!/usr/bin/env bash
# =========================================================
# TitanKV RAG 服务端到端冒烟 (M1+)
# 链路: curl → rag-service(:18085) → minikv_server(:18888, 临时DB)
# 覆盖: healthz / 同步 ingest / BM25+dense 混合检索 / 删除 / 重启恢复
# 用法: bash scripts/e2e_rag.sh
# =========================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MINIKV_BIN="${ROOT}/build/minikv/minikv_server"
GO="${GO:-$(command -v go || echo /usr/local/go/bin/go)}"
DB="$(mktemp -d)"
IDX="$(mktemp -d)"
MINIKV="127.0.0.1:18888"
RAG="127.0.0.1:18085"
MINIKV_PID=""; RAG_PID=""

cleanup() {
  [ -n "$RAG_PID" ] && kill "$RAG_PID" 2>/dev/null || true
  [ -n "$MINIKV_PID" ] && kill "$MINIKV_PID" 2>/dev/null || true
  rm -rf "$DB" "$IDX"
}
trap cleanup EXIT

fail() { echo "[E2E-FAIL] $*" >&2; exit 1; }
step() { echo; echo "===== $* ====="; }

# ---- 启动 ----
step "start minikv_server ($MINIKV, db=$DB)"
"$MINIKV_BIN" --host 127.0.0.1 --port 18888 --db "$DB" >/tmp/e2e-minikv.log 2>&1 &
MINIKV_PID=$!
sleep 1
kill -0 "$MINIKV_PID" 2>/dev/null || fail "minikv died: $(cat /tmp/e2e-minikv.log)"

step "start rag-service ($RAG)"
( cd "$ROOT" && RAG_ADDR="$RAG" MINIKV_ADDR="$MINIKV" RAG_INDEX_DIR="$IDX" \
  RAG_ASYNC_INGEST=false RAG_ENABLE_BM25=true RAG_ENABLE_WIKI=false \
  "$GO" run ./services/rag/cmd >/tmp/e2e-rag.log 2>&1 ) &
RAG_PID=$!
for i in $(seq 1 30); do
  curl -sf "http://$RAG/healthz" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "http://$RAG/healthz" | grep -q '"status":"ok"' || fail "rag healthz: $(tail -5 /tmp/e2e-rag.log)"

step "1) ingest 3 keyword-distinct docs (sync)"
ing() { curl -sf -X POST "http://$RAG/api/rag/collections/e2e/documents" \
  -H 'Content-Type: application/json' -d "$1"; }
ing '{"title":"bloom","text":"布隆过滤器通过位数组与多个哈希函数判断元素是否存在, 误判率随填充度上升, 最优哈希个数与位数组大小相关。"}' | grep -q '"status":"success"' || fail "ingest bloom"
ing '{"title":"wal","text":"WAL 预写日志先顺序追加到日志文件, 落盘成功后才修改内存, 宕机后通过重放日志恢复未持久化的修改。"}' | grep -q '"status":"success"' || fail "ingest wal"
ing '{"title":"lsm","text":"LSM-Tree 写入先进 MemTable 内存跳表, 达到阈值刷成 SSTable 落盘, 后台 Compaction 合并多层排序。"}' | grep -q '"status":"success"' || fail "ingest lsm"

# top_title: 取检索 top1 的 doc_id, 回查 title (doc_id 是 uuid, 断言看 title)
top_title() {
  curl -sf -X POST "http://$RAG/api/rag/collections/e2e/retrieve" \
    -H 'Content-Type: application/json' -d "$1" \
  | python3 -c '
import json,sys,urllib.request
hits=json.load(sys.stdin)["hits"]
if not hits: print(""); raise SystemExit
doc=hits[0]["doc_id"]
items=json.load(urllib.request.urlopen("http://'"$RAG"'/api/rag/collections/e2e/documents"))["items"]
print([x["title"] for x in items if x["doc_id"]==doc][0])'
}

step "2) BM25 keyword retrieval ranks exact-term doc first"
top=$(top_title '{"query":"布隆过滤器 位数组","top_k":3}')
echo "top1 = $top"
[ "$top" = "bloom" ] || fail "expected bloom doc first, got $top"

step "3) CJK-bigram paraphrase query hits via BM25 lane"
top=$(top_title '{"query":"为什么宕机后数据不会丢","top_k":3}')
echo "top1 = $top"
[ "$top" = "wal" ] || fail "expected wal doc first, got $top"

step "4) delete doc → retrieval no longer returns it"
doc=$(curl -sf "http://$RAG/api/rag/collections/e2e/documents" | python3 -c 'import json,sys; d=json.load(sys.stdin)["items"]; print([x["doc_id"] for x in d if x["title"]=="bloom"][0])')
curl -sf -X DELETE "http://$RAG/api/rag/collections/e2e/documents/$doc" | grep -q '"ok":true' || fail "delete"
sleep 0.2
n=$(curl -sf -X POST "http://$RAG/api/rag/collections/e2e/retrieve" \
  -H 'Content-Type: application/json' \
  -d '{"query":"布隆过滤器 位数组 哈希","top_k":5}' \
  | python3 -c '
import json,sys
hits=json.load(sys.stdin)["hits"]
print(sum(1 for h in hits if "布隆过滤器" in h["text"]))')
[ "$n" -eq 0 ] || fail "bloom chunks still retrievable after delete ($n hits)"

step "5) restart rag-service → BM25 postings restore from minikv"
kill "$RAG_PID"; RAG_PID=""; sleep 1
( cd "$ROOT" && RAG_ADDR="$RAG" MINIKV_ADDR="$MINIKV" RAG_INDEX_DIR="$IDX" \
  RAG_ASYNC_INGEST=false RAG_ENABLE_BM25=true RAG_ENABLE_WIKI=false \
  "$GO" run ./services/rag/cmd >>/tmp/e2e-rag.log 2>&1 ) &
RAG_PID=$!
for i in $(seq 1 30); do curl -sf "http://$RAG/healthz" >/dev/null 2>&1 && break; sleep 1; done
top=$(top_title '{"query":"预写日志 顺序追加 重放","top_k":3}')
echo "top1 = $top"
[ "$top" = "wal" ] || fail "bm25 not restored after restart"

echo
echo "[E2E-PASS] RAG 服务端到端 5 步全部通过 (ingest → 混合检索 → 改述命中 → 删除 → 重启恢复)"
