#!/usr/bin/env bash
# Manual live smoke for the Phase E4 engine-owned lifecycle + liveness feed
# (ADR-0052): one-verb POST /open, session open-or-resume, atomic accept, and
# corpus progress on GET /events with no client poll loop. NOT part of `go
# test`: it needs a running engine (default http://127.0.0.1:9100, override
# ENGINE_URL) with the control daemon up and an embedding model provisioned
# (the corpus index resolves it to embed chunks).
#
# It exercises the exact verbs the Ratatui TUI bridge now calls:
#   1. POST /open on a directory -> bounded listing + workspace
#   2. POST /open on a file      -> document + blocks + session + modes + workspace
#   3. POST /documents/{id}/session -> newest resume; anchor -> distinct session
#   4. GET /events receives corpus progress while POST /corpus/index runs
#   5. POST /documents/{id}/blocks/{bid}/accept writes through
#   6. an external change is the typed 409, never a clobber
#
# The engine owns ALLOWED_ROOTS, so the temp vault MUST live under an allowed
# root — macOS `mktemp -d` defaults to /var/folders, which is outside it. Set
# SMOKE_BASE to an allowed directory (default $HOME).
#
# Usage: ENGINE_URL=http://127.0.0.1:9100 tools/smoke-lifecycle.sh
#        SMOKE_BASE=/path/under/allowed/roots tools/smoke-lifecycle.sh
set -euo pipefail

ENGINE_URL="${ENGINE_URL:-http://127.0.0.1:9100}"
SMOKE_BASE="${SMOKE_BASE:-$HOME}"

tmpdir="$(mktemp -d "${SMOKE_BASE%/}/texteditor-smoke-lifecycle.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

vault="$tmpdir/vault"
note="$vault/note.md"
mkdir -p "$vault"
printf 'The original sentence.\n' >"$note"

if ! curl -fsS -m 3 "$ENGINE_URL/health" >/dev/null 2>&1; then
  echo "FAIL: engine not reachable at $ENGINE_URL" >&2
  echo "Start it first (the control daemon must be up, see README 'Use the TUI'):" >&2
  echo "  cd server && go run ./cmd/texteditor --port 9100" >&2
  exit 1
fi

post() {
  local body="${2:-}"
  [ -n "$body" ] || body='{}'
  curl -fsS -X POST "$ENGINE_URL$1" -H 'content-type: application/json' -d "$body"
}
# json_field JSON_PATH: read stdin JSON and print the value at the path.
json_field() {
  python3 -c "import json,sys; print(json.load(sys.stdin)$1)"
}

echo "engine: $ENGINE_URL"
echo "vault:  $vault"

# 1. POST /open on a directory.
dir_json="$(post /open "$(python3 - "$vault" <<'PY'
import json, sys
print(json.dumps({"path": sys.argv[1]}))
PY
)")"
printf '%s' "$dir_json" | python3 -c '
import json, sys
r = json.load(sys.stdin)
assert r["kind"] == "directory", r
assert r.get("listing") and r["listing"].get("entries") is not None, r
assert r["workspace"]["id"], r
print("PASS: /open directory -> bounded listing + workspace")
'

# 2. POST /open on a file.
open_json="$(post /open "$(python3 - "$note" <<'PY'
import json, sys
print(json.dumps({"path": sys.argv[1]}))
PY
)")"
doc_id="$(printf '%s' "$open_json" | json_field '["document"]["id"]')"
session_id="$(printf '%s' "$open_json" | json_field '["session"]["id"]')"
ws_id="$(printf '%s' "$open_json" | json_field '["workspace"]["id"]')"
block_id="$(printf '%s' "$open_json" | json_field '["blocks"][0]["id"]')"
printf '%s' "$open_json" | python3 -c '
import json, sys
r = json.load(sys.stdin)
assert r["kind"] == "document", r
assert r.get("document") and r.get("blocks") and r.get("session") and r.get("modes"), r
assert r["workspace"]["id"], r
print("PASS: /open file -> document + blocks + session + modes + workspace")
'

# 3. Session open-or-resume: no anchor resumes the newest; an anchor is distinct.
s2="$(post "/documents/$doc_id/session" '{}' | json_field '["id"]')"
if [ "$s2" != "$session_id" ]; then
  echo "FAIL: open-or-resume changed the session ($s2 vs $session_id)" >&2
  exit 1
fi
anchor="b-anchor"
sa="$(post "/documents/$doc_id/session" "$(python3 - "$anchor" <<'PY'
import json, sys
print(json.dumps({"anchorBlockId": sys.argv[1]}))
PY
)" | json_field '["id"]')"
if [ "$sa" = "$session_id" ]; then
  echo "FAIL: anchor did not select a distinct session" >&2
  exit 1
fi
echo "PASS: session open-or-resume (newest) and anchor-keyed create"

# 4. Liveness feed: subscribe, start an index, observe a corpus event.
events_file="$tmpdir/events.log"
curl -sN "$ENGINE_URL/events?workspaceId=$ws_id" >"$events_file" 2>/dev/null &
feed_pid=$!
sleep 0.5
post /corpus/index "$(python3 - "$ws_id" <<'PY'
import json, sys
print(json.dumps({"workspaceId": sys.argv[1]}))
PY
)" >/dev/null
ok=0
for _ in $(seq 1 60); do
  if grep -q '^event: corpus' "$events_file"; then
    ok=1
    break
  fi
  sleep 0.25
done
kill "$feed_pid" 2>/dev/null || true
if [ "$ok" != 1 ]; then
  echo "FAIL: no corpus event arrived on /events" >&2
  cat "$events_file" >&2 || true
  exit 1
fi
echo "PASS: corpus progress arrived on /events with no client poll loop"

# 5. Atomic accept: stage a candidate, then accept (client sends no text).
post "/documents/$doc_id/edits" "$(python3 - "$block_id" <<'PY'
import json, sys
print(json.dumps({"blockId": sys.argv[1], "text": "The rewritten sentence."}))
PY
)" >/dev/null
accept_json="$(post "/documents/$doc_id/blocks/$block_id/accept" '{}')"
printf '%s' "$accept_json" | python3 -c '
import json, sys
r = json.load(sys.stdin)
assert r.get("writtenThrough") is True, r
assert r.get("path"), r
print("PASS: accept wrote through")
'
if ! grep -q 'The rewritten sentence' "$note"; then
  echo "FAIL: the accepted edit did not reach the file" >&2
  exit 1
fi

# 6. External change -> typed 409, no clobber.
printf 'changed externally\n' >"$note"
post "/documents/$doc_id/edits" "$(python3 - "$block_id" <<'PY'
import json, sys
print(json.dumps({"blockId": sys.argv[1], "text": "Second rewrite."}))
PY
)" >/dev/null
code="$(curl -sS -o "$tmpdir/conflict.json" -w '%{http_code}' \
  -X POST "$ENGINE_URL/documents/$doc_id/blocks/$block_id/accept" \
  -H 'content-type: application/json' -d '{}')"
if [ "$code" != "409" ]; then
  echo "FAIL: accept over an external change = $code, want 409" >&2
  cat "$tmpdir/conflict.json" >&2 || true
  exit 1
fi
if ! grep -q 'file-changed-externally' "$tmpdir/conflict.json"; then
  echo "FAIL: not the typed file-changed-externally body" >&2
  cat "$tmpdir/conflict.json" >&2
  exit 1
fi
if ! grep -q 'changed externally' "$note"; then
  echo "FAIL: the external change was clobbered" >&2
  exit 1
fi
echo "PASS: external change refused with file-changed-externally; no clobber"

echo "OK: E4 lifecycle + liveness smoke passed"
