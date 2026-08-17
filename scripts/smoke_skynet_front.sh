#!/usr/bin/env bash
# Smoke: Client → skynet_gateway(:8080) → Gin(:18080) → /ping + /healthz
# Does not require Auth/Data/minikv — only proves the front-proxy path.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKYNET_BIN="${SKYNET_BIN:-$ROOT/build/skynet/skynet_gateway}"
SKYNET_CFG="${SKYNET_CFG:-$ROOT/skynet/gateway/gateway.yaml}"
GIN_ADDR=":18080"
PUBLIC_PORT=8080

fuser -k "${PUBLIC_PORT}/tcp" "18080/tcp" >/dev/null 2>&1 || true
sleep 0.3

if [[ ! -x "$SKYNET_BIN" ]]; then
  (cd "$ROOT" && make cmake-build)
fi

(cd "$ROOT" && go build -o /tmp/titankv_gin_gw ./cmd/gateway)

GATEWAY_ADDR="$GIN_ADDR" /tmp/titankv_gin_gw > /tmp/titankv_smoke_gin.log 2>&1 &
GIN_PID=$!

cleanup() {
  kill "$SKY_PID" "$GIN_PID" 2>/dev/null || true
  wait "$SKY_PID" "$GIN_PID" 2>/dev/null || true
}
trap cleanup EXIT

# Wait for Gin first (upstream must be ready before skynet health/proxy)
ok=0
for i in $(seq 1 40); do
  if curl -sS -m 1 "http://127.0.0.1:18080/ping" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 0.25
done
if [[ "$ok" != 1 ]]; then
  echo "FAIL: Gin did not become ready on :18080"
  tail -n 40 /tmp/titankv_smoke_gin.log || true
  exit 1
fi

"$SKYNET_BIN" --config "$SKYNET_CFG" > /tmp/titankv_smoke_skynet.log 2>&1 &
SKY_PID=$!

ok=0
for i in $(seq 1 40); do
  if curl -sS -m 1 "http://127.0.0.1:${PUBLIC_PORT}/ping" >/tmp/titankv_smoke_ping.out 2>/dev/null; then
    if grep -q '"pong"' /tmp/titankv_smoke_ping.out; then
      ok=1
      break
    fi
  fi
  sleep 0.25
done
if [[ "$ok" != 1 ]]; then
  echo "FAIL: skynet front did not proxy /ping correctly on :${PUBLIC_PORT}"
  echo "body: $(cat /tmp/titankv_smoke_ping.out 2>/dev/null || true)"
  echo "--- skynet log ---"
  tail -n 40 /tmp/titankv_smoke_skynet.log || true
  echo "--- gin log ---"
  tail -n 40 /tmp/titankv_smoke_gin.log || true
  exit 1
fi

curl -sS -m 3 "http://127.0.0.1:${PUBLIC_PORT}/healthz" | grep -q '"status":"ok"'

ss -ltn | grep -q ':18080'
ss -ltn | grep -q ':8080'
kill -0 "$SKY_PID"
kill -0 "$GIN_PID"

echo "SMOKE PASS: Client → skynet(:8080) → Gin(:18080) /ping+/healthz"
