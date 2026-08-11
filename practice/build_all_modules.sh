#!/usr/bin/env bash
# ??? moduleXX_* ????????????? build/ ??
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

JOBS="$(nproc 2>/dev/null || echo 4)"
FAILED=()

shopt -s nullglob
for dir in module*/; do
  M="${dir%/}"
  # ????????
  [[ "$M" == module* ]] || continue
  echo "======== BUILD $M ========"
  cmake -B "$ROOT/$M/build" -S "$ROOT/$M" -DCMAKE_BUILD_TYPE=Release
  cmake --build "$ROOT/$M/build" -j"$JOBS"
  if [[ ! -x "$ROOT/$M/build/$M" ]]; then
    echo "ERROR: missing binary $ROOT/$M/build/$M" >&2
    FAILED+=("$M")
  else
    echo "OK: $ROOT/$M/build/$M"
  fi
done

if ((${#FAILED[@]})); then
  echo "Build failed for:${FAILED[*]}" >&2
  exit 1
fi
echo "All modules built successfully."

