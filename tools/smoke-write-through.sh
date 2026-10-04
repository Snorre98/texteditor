#!/usr/bin/env bash
# Manual live-model smoke for ADR-0047 write-through. NOT part of `go test`:
# it needs a running engine (default http://127.0.0.1:9100, override ENGINE_URL)
# with the control daemon and a live model serving the `proofreader` preset.
#
# It exercises the acceptance path end to end:
#   open temp note -> one turn (model stages an edit_markdown candidate) ->
#   external edit -> commit must 409 with no clobber -> overwrite -> file changed.
#
# Usage: ENGINE_URL=http://127.0.0.1:9100 tools/smoke-write-through.sh
set -euo pipefail

ENGINE_URL="${ENGINE_URL:-http://127.0.0.1:9100}"
tmpdir="$(mktemp -d)"
NOTE="$tmpdir/smoke.md"
CONFLICT_BODY="$tmpdir/conflict.json"
trap 'rm -rf "$tmpdir"' EXIT

if ! curl -fsS -m 3 "$ENGINE_URL/health" >/dev/null 2>&1; then
  echo "FAIL: engine not reachable at $ENGINE_URL" >&2
  echo "Start it first (the control daemon must be up, see README 'Use the TUI'):" >&2
  echo "  cd server && go run ./cmd/texteditor --port 9100" >&2
  echo "or point the script at another engine: ENGINE_URL=http://host:port $0" >&2
  exit 1
fi

printf 'The quick brown fox jumps over the lazy dog.\n' >"$NOTE"

post() { curl -fsS -X POST "$ENGINE_URL$1" -H 'content-type: application/json' -d "${2:-{}}"; }

echo "engine: $ENGINE_URL"
echo "note:   $NOTE"

doc_id="$(post /documents "{\"path\":\"$NOTE\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "document: $doc_id"

# One turn: ask for a rewrite. The SSE stream is drained; the candidate is
# staged engine-side.
curl -fsS -N -X POST "$ENGINE_URL/turn" -H 'content-type: application/json' \
  -d "{\"sessionId\":\"smoke-$doc_id\",\"modeName\":\"proofreader\",\"documentId\":\"$doc_id\",\"userInput\":\"Rewrite the paragraph to be more concise.\"}" \
  >/dev/null

block_id="$(curl -fsS "$ENGINE_URL/documents/$doc_id/blocks" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')"
candidate_count="$(curl -fsS "$ENGINE_URL/documents/$doc_id/blocks/$block_id/candidates" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"
if [ "$candidate_count" -eq 0 ]; then
  echo "FAIL: the turn staged no candidate (is a live model serving proofreader?)"
  exit 1
fi
echo "candidate staged for block $block_id"

# External edit between stage and accept: the write-through must be refused.
printf 'externally edited while the engine held a candidate\n' >"$NOTE"
code="$(curl -sS -o "$CONFLICT_BODY" -w '%{http_code}' -X POST \
  "$ENGINE_URL/documents/$doc_id/commits" -H 'content-type: application/json' -d '{}')"
if [ "$code" != "409" ]; then
  echo "FAIL: commit over an external change = HTTP $code, want 409"
  cat "$CONFLICT_BODY"
  exit 1
fi
if ! grep -q 'file-changed-externally' "$CONFLICT_BODY"; then
  echo "FAIL: 409 body missing file-changed-externally"
  cat "$CONFLICT_BODY"
  exit 1
fi
if [ "$(cat "$NOTE")" != "externally edited while the engine held a candidate" ]; then
  echo "FAIL: the conflict clobbered the file"
  exit 1
fi
echo "PASS: external change -> 409, no clobber"

# Explicit overwrite accepts the write and mirrors the canonical bytes.
post "/documents/$doc_id/commits" '{"overwrite":true}' >/dev/null
echo "--- $NOTE (after overwrite)"
cat "$NOTE"
if [ "$(cat "$NOTE")" = "externally edited while the engine held a candidate" ]; then
  echo "FAIL: overwrite did not change the file"
  exit 1
fi
echo "PASS: overwrite mirrored the accepted edit to disk"
