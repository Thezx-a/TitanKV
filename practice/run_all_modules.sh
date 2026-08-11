#!/usr/bin/env bash
# ????? module ??????????? build_all_modules.sh?
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

FAILED=()
shopt -s nullglob
for dir in module*/; do
  M="${dir%/}"
  BIN="$ROOT/$M/build/$M"
  echo "======== RUN $M ========"
  if [[ ! -x "$BIN" ]]; then
    echo "ERROR: binary not found: $BIN (run ./build_all_modules.sh first)" >&2
    FAILED+=("$M")
    continue
  fi
  if ! "$BIN"; then
    echo "ERROR: $M exited non-zero" >&2
    FAILED+=("$M")
  fi
done

if ((${#FAILED[@]})); then
  echo "Run failed for:${FAILED[*]}" >&2
  exit 1
fi
echo "All modules ran successfully."

