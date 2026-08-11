#!/usr/bin/env bash
# End-to-end smoke: minikv + data service Put/Get + restart recovery.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/build/minikv/minikv_server"
DB="${TMPDIR:-/tmp}/titankv_smoke_db"
PORT=19888
DATA_PORT=19881
ADDR="127.0.0.1:${PORT}"

# Best-effort cleanup of previous smoke runs
fuser -k "${PORT}/tcp" "${DATA_PORT}/tcp" >/dev/null 2>&1 || true
sleep 0.2

if [[ ! -x "$BIN" ]]; then
  (cd "$ROOT" && make cmake-build)
fi

(cd "$ROOT" && go build -o /tmp/titankv_data_svc ./cmd/data)

rm -rf "$DB"
mkdir -p "$DB"

"$BIN" --host 127.0.0.1 --port "$PORT" --db "$DB" > /tmp/titankv_smoke_minikv.log 2>&1 &
MK_PID=$!
DATA_PID=""
cleanup() {
  [[ -n "${DATA_PID}" ]] && kill "$DATA_PID" 2>/dev/null || true
  kill "$MK_PID" 2>/dev/null || true
}
trap cleanup EXIT
sleep 0.3

MINIKV_ADDR="$ADDR" DATA_ADDR=":${DATA_PORT}" /tmp/titankv_data_svc \
  > /tmp/titankv_smoke_data.log 2>&1 &
DATA_PID=$!
sleep 0.4

curl -m 3 -sf "http://127.0.0.1:${DATA_PORT}/healthz" | grep -q minikv
curl -m 3 -sf -X POST "http://127.0.0.1:${DATA_PORT}/api/data/kv" \
  -H 'Content-Type: application/json' \
  -d '{"key":"smoke","value":"ok"}' | grep -q '"ok":true'
curl -m 3 -sf "http://127.0.0.1:${DATA_PORT}/api/data/kv?key=smoke" | grep -q '"value":"ok"'

# Restart engine — Manifest + WAL recovery
kill -9 "$DATA_PID" "$MK_PID" 2>/dev/null || true
DATA_PID=""
sleep 0.3
"$BIN" --host 127.0.0.1 --port "$PORT" --db "$DB" > /tmp/titankv_smoke_minikv.log 2>&1 &
MK_PID=$!
sleep 0.3
MINIKV_ADDR="$ADDR" DATA_ADDR=":${DATA_PORT}" /tmp/titankv_data_svc \
  > /tmp/titankv_smoke_data.log 2>&1 &
DATA_PID=$!
sleep 0.4
curl -m 3 -sf "http://127.0.0.1:${DATA_PORT}/api/data/kv?key=smoke" | grep -q '"value":"ok"'

echo "SMOKE PASS: Put/Get + restart recovery via MINIKV_ADDR"
