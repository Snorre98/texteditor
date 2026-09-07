#!/usr/bin/env bash
# Build the Tauri desktop editor: verification gates -> the Go engine rebuilt as
# the bundled sidecar -> bun deps -> `tauri build` (.app/.dmg). The one entry
# point for the desktop bundle (ADR-0041). Run from the repo root or tools/.
#
# The sidecar is ALWAYS rebuilt from server/ source first — this is the fix for
# the stale-binary trap (an app whose UI is fresh but whose engine is not).
#
# Flags:
#   --skip-gates     skip go/bun/cargo test gates (ADR-0041 §3)
#   --sidecar-only   rebuild only the sidecar binary, then print the tauri:dev
#                    hint (the dev-refresh path; no bundle is produced)
#
# Env knobs (respected, never auto-set — ADR-0041 §4):
#   RUSTUP_HOME/CARGO_HOME/PATH — where cargo lives (this machine keeps Rust on
#                    the external SSD; export the SSD paths if `cargo` is not
#                    on PATH, see client/tauri/README.md)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TAURI_DIR="$REPO_ROOT/client/tauri"
DAEMON_URL="${DAEMON_URL:-http://127.0.0.1:9300}"

SKIP_GATES=0
SIDECAR_ONLY=0
for arg in "$@"; do
  case "$arg" in
    --skip-gates) SKIP_GATES=1 ;;
    --sidecar-only) SIDECAR_ONLY=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

# ---- 1. Toolchain ------------------------------------------------------------
for cmd in go bun; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "error: '$cmd' not on PATH" >&2; exit 1; }
done
if ! command -v cargo >/dev/null 2>&1; then
  echo "error: 'cargo' not on PATH. Rust lives on the external SSD here; export it first:" >&2
  echo "  export RUSTUP_HOME=/Volumes/Ex-SSD/caches/rust CARGO_HOME=/Volumes/Ex-SSD/caches/cargo" >&2
  echo "  export PATH=\"\$CARGO_HOME/bin:\$PATH\"" >&2
  exit 1
fi

# ---- 2. Verification gates (ADR-0041 §3) --------------------------------------
if [[ "$SKIP_GATES" -eq 0 && "$SIDECAR_ONLY" -eq 0 ]]; then
  echo "== gates: engine tests"
  (cd "$REPO_ROOT/server" && CGO_ENABLED=0 go test ./...)

  echo "== gates: client tests + typecheck"
  (cd "$TAURI_DIR" && bun test && bun run typecheck)

  echo "== gates: sidecar handshake tests (cargo)"
  if curl -fsS --max-time 2 "$DAEMON_URL/list" >/dev/null 2>&1; then
    (cd "$TAURI_DIR/src-tauri" && cargo test)
  else
    echo "warn: control daemon unreachable at $DAEMON_URL — skipping cargo test" >&2
    echo "      (the sidecar handshake test spawns the engine, which fails fast" >&2
    echo "      without the daemon — ADR-0025; start fleetdaemon and re-run)" >&2
  fi
fi

# ---- 3. Sidecar — always rebuilt from source (ADR-0041 §2, ADR-0021 §1) --------
echo "== sidecar: build engine for the host target triple"
HOST_TRIPLE="$(rustc -vV | awk '/^host:/{print $2}')"
SIDECAR="$TAURI_DIR/src-tauri/binaries/texteditor-$HOST_TRIPLE"
mkdir -p "$(dirname "$SIDECAR")"
(
  cd "$REPO_ROOT/server"
  CGO_ENABLED=0 go build -o "$SIDECAR" ./cmd/texteditor
)
echo "   sidecar: $SIDECAR ($(du -h "$SIDECAR" | cut -f1))"

if [[ "$SIDECAR_ONLY" -eq 1 ]]; then
  echo "sidecar refreshed — dev run (control daemon must be up):"
  echo "  cd $TAURI_DIR && bun run tauri:dev"
  exit 0
fi

# ---- 4. Frontend deps + Tauri bundle (ADR-0041 §5) ----------------------------
echo "== deps: bun install (frozen lockfile)"
(cd "$TAURI_DIR" && bun install --frozen-lockfile)

echo "== bundle: tauri build (frontend build runs via beforeBuildCommand)"
(cd "$TAURI_DIR" && bun run tauri:build)

# ---- 5. Report -----------------------------------------------------------------
BUNDLE_DIR="$TAURI_DIR/src-tauri/target/release/bundle"
echo
echo "built:"
if [[ -d "$BUNDLE_DIR" ]]; then
  find "$BUNDLE_DIR" -maxdepth 2 \( -name "*.app" -o -name "*.dmg" \) ! -name "rw.*" -print | sed 's/^/  /'
else
  echo "  $BUNDLE_DIR"
fi
