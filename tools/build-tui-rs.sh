#!/usr/bin/env bash
# Build the standalone Ratatui TUI (client/tui-rs) with plain cargo
# (ADR-0046 §10, ADR-0043 §4). Run from the repo root or tools/.
#
# Flags:
#   --release   build the optimized binary
#   --test      run `cargo test` first (the real gate lands in E3)
#
# Env knobs (respected, never auto-set — ADR-0043 §4):
#   RUSTUP_HOME / CARGO_HOME / PATH — where cargo lives. This machine keeps Rust
#   on the external SSD; export the SSD paths if `cargo` is not on PATH:
#     export RUSTUP_HOME=/Volumes/Ex-SSD/caches/rust CARGO_HOME=/Volumes/Ex-SSD/caches/cargo
#     export PATH="$CARGO_HOME/bin:$PATH"
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CRATE_DIR="$REPO_ROOT/client/tui-rs"

RELEASE=0
RUN_TESTS=0
for arg in "$@"; do
  case "$arg" in
    --release) RELEASE=1 ;;
    --test) RUN_TESTS=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

if ! command -v cargo >/dev/null 2>&1; then
  echo "error: 'cargo' not on PATH. Rust lives on the external SSD here; export it first:" >&2
  echo "  export RUSTUP_HOME=/Volumes/Ex-SSD/caches/rust CARGO_HOME=/Volumes/Ex-SSD/caches/cargo" >&2
  echo "  export PATH=\"\$CARGO_HOME/bin:\$PATH\"" >&2
  exit 1
fi

if [[ "$RUN_TESTS" -eq 1 ]]; then
  echo "== gates: cargo test ($CRATE_DIR)"
  (cd "$CRATE_DIR" && cargo test)
fi

BUILD_ARGS=(build)
PROFILE_DIR="debug"
if [[ "$RELEASE" -eq 1 ]]; then
  BUILD_ARGS+=(--release)
  PROFILE_DIR="release"
fi

echo "== build: cargo ${BUILD_ARGS[*]}"
(cd "$CRATE_DIR" && cargo "${BUILD_ARGS[@]}")

echo "built: $CRATE_DIR/target/$PROFILE_DIR/texteditor-tui-rs"
