#!/usr/bin/env bash
# Install the writing-assistant engine as a per-user launchd agent (KeepAlive) —
# the "standalone daemon" deploy target (ADR-0014 §2, Plan E1).
#
# Defaults to a fixed localhost port (9100 = the spec servers[0] / TUI default)
# so local clients reach the daemon without discovery. Override with
# ENGINE_PORT=<port> to pin a different port. The dynamic-port mode
# (ENGINE_PORT=0) is intended for the Tauri sidecar, whose Rust core reads the
# actual base URL from /health (ADR-0021 §1) — not for a standalone daemon.
#
# Env knobs:
#   ENGINE_PORT        port to pin (default 9100)
#   DAEMON_URL         control-daemon base URL (default http://127.0.0.1:9300)
#   TEXTEDITOR_DATA_DIR  engine data dir (default ~/.local/share/texteditor)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="$HOME/.local/bin"
DATA_DIR="${TEXTEDITOR_DATA_DIR:-$HOME/.local/share/texteditor}"
DAEMON_URL="${DAEMON_URL:-http://127.0.0.1:9300}"
PORT="${ENGINE_PORT:-9100}"
PLIST_TMP="/tmp/com.texteditor.engine.plist"
PLIST_DEST="$HOME/Library/LaunchAgents/com.texteditor.engine.plist"

"$REPO_ROOT/tools/build.sh"

mkdir -p "$BIN_DIR" "$DATA_DIR"
install -m 0755 "$REPO_ROOT/bin/texteditor" "$BIN_DIR/texteditor"

sed \
  -e "s|__BIN__|$BIN_DIR/texteditor|" \
  -e "s|__DATA__|$DATA_DIR|" \
  -e "s|__DAEMON_URL__|$DAEMON_URL|" \
  -e "s|__PORT__|$PORT|" \
  "$REPO_ROOT/deploy/com.texteditor.engine.plist" > "$PLIST_TMP"

mkdir -p "$HOME/Library/LaunchAgents"
cp "$PLIST_TMP" "$HOME/Library/LaunchAgents/com.texteditor.engine.plist"

launchctl unload "$PLIST_DEST" 2>/dev/null || true
launchctl load "$PLIST_DEST"

echo "installed + loaded com.texteditor.engine (port $PORT, data $DATA_DIR)"
