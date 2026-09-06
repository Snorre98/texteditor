#!/usr/bin/env bash
# Build the writing-assistant engine into bin/texteditor — a single static Go
# binary, no CGO (ADR-0003). Run from the repo root or server/.
set -euo pipefail

# Resolve the repo root (the directory holding server/ and api/).
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mkdir -p "$REPO_ROOT/bin"
(
  cd "$REPO_ROOT/server"
  CGO_ENABLED=0 go build -o "$REPO_ROOT/bin/texteditor" ./cmd/texteditor
)
echo "built $REPO_ROOT/bin/texteditor"
