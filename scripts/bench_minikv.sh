#!/usr/bin/env bash
# Local benchmark for minikv TCP Put/Get + kBatch (WriteBatch).
# Usage: ./scripts/bench_minikv.sh [host:port] [n] [batch_size]
#   n           = number of keys for single Put/Get and total keys for batch (default 5000)
#   batch_size  = ops per kBatch round-trip (default 100)
set -euo pipefail
ADDR="${1:-127.0.0.1:8888}"
N="${2:-5000}"
BATCH="${3:-100}"
HOST="${ADDR%%:*}"
PORT="${ADDR##*:}"

python3 - "$HOST" "$PORT" "$N" "$BATCH" <<'PY'
import socket, struct, sys, time
host, port, n, batch_size = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), int(sys.argv[4])

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
    return st, body

def encode_batch(ops):
    # count:u32 | repeated (op:u8, key_len:u32, key, val_len:u32, val)
    # op: 1=Put, 2=Delete (BatchOpType)
    parts = [struct.pack("<I", len(ops))]
    for put, key, val in ops:
        parts.append(struct.pack("<B", 1 if put else 2))
        parts.append(struct.pack("<I", len(key)))
        parts.append(key)
        parts.append(struct.pack("<I", len(val)))
        parts.append(val)
    return b"".join(parts)

s = socket.create_connection((host, port), 5)
val = b"x" * 64

# --- single Put ---
t0 = time.perf_counter()
for i in range(n):
    st, _ = rt(s, 1, f"b{i:06d}".encode(), val)
    if st != 0:
        raise SystemExit(f"put fail at {i} st={st}")
t1 = time.perf_counter()

# --- single Get ---
for i in range(n):
    st, _ = rt(s, 2, f"b{i:06d}".encode())
    if st != 0:
        raise SystemExit(f"get miss at {i} st={st}")
t2 = time.perf_counter()

# --- kBatch Put (chunked) ---
# Use distinct key prefix so we don't measure overwrite-only.
n_batch = n - (n % batch_size)  # align
if n_batch == 0:
    raise SystemExit("n must be >= batch_size")
batches = n_batch // batch_size
t3 = time.perf_counter()
for b in range(batches):
    ops = []
    for j in range(batch_size):
        i = b * batch_size + j
        ops.append((True, f"w{i:06d}".encode(), val))
    payload = encode_batch(ops)
    st, body = rt(s, 5, b"", payload)  # Cmd::kBatch = 5
    if st != 0:
        raise SystemExit(f"batch put fail at batch {b} st={st} body={body!r}")
t4 = time.perf_counter()

# verify a sample via Get
st, _ = rt(s, 2, f"w{0:06d}".encode())
if st != 0:
    raise SystemExit("batch verify get miss")
st, _ = rt(s, 2, f"w{n_batch-1:06d}".encode())
if st != 0:
    raise SystemExit("batch verify get miss last")

s.close()
put_s, get_s, batch_s = t1 - t0, t2 - t1, t4 - t3
print(f"n={n} value=64B batch_size={batch_size} batch_keys={n_batch} batches={batches}")
print(f"put:       {n/put_s:.0f} ops/s  avg={put_s*1e6/n:.1f} us")
print(f"get:       {n/get_s:.0f} ops/s  avg={get_s*1e6/n:.1f} us")
print(f"batch_put: {n_batch/batch_s:.0f} ops/s  avg_per_key={batch_s*1e6/n_batch:.1f} us  "
      f"rtt={batches/batch_s:.0f} batch/s  avg_batch={batch_s*1e3/batches:.2f} ms")
print(f"put_total_s={put_s:.3f} get_total_s={get_s:.3f} batch_total_s={batch_s:.3f}")
# machine-readable line for docs
print(f"CSV put_ops_s={n/put_s:.0f} get_ops_s={n/get_s:.0f} "
      f"batch_put_ops_s={n_batch/batch_s:.0f} batch_size={batch_size}")
PY
