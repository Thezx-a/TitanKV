#!/usr/bin/env bash
# smoke_wiki.sh — unit-level TitanWiki smoke (no live minikv required).
# Full HTTP smoke needs minikv + rag up; this script gates on go tests.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export PATH=/usr/local/go/bin:${PATH:-}
cd "$ROOT"

echo "[smoke_wiki] go test wiki suite"
go test ./services/rag/ -count=1 -run 'TestSlugify|TestWiki|TestExtract|TestCompile|TestAssembleWiki|TestDeleteDocumentClearsWiki'

echo "[smoke_wiki] go build rag cmd"
go build -o /tmp/rag-svc-wiki ./services/rag/cmd/

echo "[smoke_wiki] PASS"
