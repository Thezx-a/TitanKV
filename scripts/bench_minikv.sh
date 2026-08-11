#!/usr/bin/env bash
# Simple local benchmark for minikv TCP Put/Get.
# Usage: ./scripts/bench_minikv.sh [host:port] [n]
set -euo pipefail
ADDR="${1:-127.0.0.1:8888}"
N="${2:-5000}"
HOST="${ADDR%%:*}"
PORT="${ADDR##*:}"

python3 - "$HOST" "$PORT" "$N" <<'PY'
import socket, struct, sys, time
host, port, n = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])

def enc(cmd, key=b"", val=b""):
    return struct.pack("<HBII", 0x4D4B, cmd, len(key), len(val)) + key + val

def rt(s, cmd, key=b"", val=b""):
    s.sendall(enc(cmd, key, val))
    hdr = b""
    while len(hdr) < 7:
        c = s.recv(7 - len(hdr))
        if not c:
            raise RuntimeError("eof")
        hdr += c
    _, st, ln = struct.unpack("<HBI", hdr)
    body = b""
    while len(body) < ln:
        c = s.recv(ln - len(body))
        if not c:
            raise RuntimeError("eof body")
        body += c
    return st

s = socket.create_connection((host, port), 5)
val = b"x" * 64
t0 = time.perf_counter()
for i in range(n):
    rt(s, 1, f"b{i:06d}".encode(), val)
t1 = time.perf_counter()
for i in range(n):
    st = rt(s, 2, f"b{i:06d}".encode())
    if st != 0:
        raise SystemExit(f"get miss at {i}")
t2 = time.perf_counter()
s.close()
put_s, get_s = t1 - t0, t2 - t1
print(f"n={n} value=64B")
print(f"put: {n/put_s:.0f} ops/s  avg={put_s*1e6/n:.1f} us")
print(f"get: {n/get_s:.0f} ops/s  avg={get_s*1e6/n:.1f} us")
print(f"put_total_s={put_s:.3f} get_total_s={get_s:.3f}")
PY
