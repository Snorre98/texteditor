#!/usr/bin/env bash
# Manual live-model smoke for ADR-0047 write-through. NOT part of `go test`:
# it needs a running engine (default http://127.0.0.1:9100, override ENGINE_URL)
# with the control daemon and a live model tagged `editor` (the only shipped
# preset that dispatches edit_markdown — proofreader/grammar are non-agentic).
#
# It exercises the acceptance path end to end:
#   open temp note -> one turn (model stages an edit_markdown candidate) ->
#   external edit -> commit must 409 with no clobber -> overwrite -> file changed.
#
# The turn input names the target block's id/hash because the as-built native
# path does not yet inject document blocks into the prompt (ADR-0029 §6's edit
# read path lands with Phase B/C); the model only has to echo them into the tool
# call, which the edit_markdown schema now carries as the base-hash guard.
#
# If the serving stack cannot emit structured tool_calls (e.g. an older
# mlx-lm server that streams the call as content), set SMOKE_STAGE_DIRECT=1:
# the script then stages the edit over `POST /documents/{id}/edits` — the same
# HTTP route the tool handler uses — and still exercises the commit/conflict/
# overwrite boundary. The model tool-call leg is skipped and labeled.
#
# Usage: ENGINE_URL=http://127.0.0.1:9100 tools/smoke-write-through.sh
#        SMOKE_STAGE_DIRECT=1 tools/smoke-write-through.sh
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

post() {
  local body="${2:-}"
  [ -n "$body" ] || body='{}'
  curl -fsS -X POST "$ENGINE_URL$1" -H 'content-type: application/json' -d "$body"
}

echo "engine: $ENGINE_URL"
echo "note:   $NOTE"

doc_id="$(post /documents "{\"path\":\"$NOTE\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "document: $doc_id"

# Read the target block (id + guard hash) so the turn can name them.
blocks_json="$(curl -fsS "$ENGINE_URL/documents/$doc_id/blocks")"
block_id="$(printf '%s' "$blocks_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')"
block_hash="$(printf '%s' "$blocks_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["hash"])')"
block_text="$(printf '%s' "$blocks_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["text"])')"

# One turn: ask for a rewrite through edit_markdown. The SSE stream is drained;
# the candidate is staged engine-side.
turn_body="$(python3 - "$doc_id" "$block_id" "$block_hash" "$block_text" <<'PY'
import json, sys
doc, bid, bh, text = sys.argv[1:5]
print(json.dumps({
    "sessionId": f"smoke-{doc}",
    "modeName": "editor",
    "documentId": doc,
    "userInput": (
        "Rewrite the paragraph to be more concise. Call the edit_markdown tool "
        f"with blockId {bid}, baseHash {bh}, and replace this text: {text}"
    ),
}))
PY
)"
curl -fsS -N -X POST "$ENGINE_URL/turn" -H 'content-type: application/json' -d "$turn_body" >/dev/null

candidate_count="$(curl -fsS "$ENGINE_URL/documents/$doc_id/blocks/$block_id/candidates" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"
if [ "$candidate_count" -eq 0 ]; then
  if [ "${SMOKE_STAGE_DIRECT:-0}" != "1" ]; then
    echo "FAIL: the turn staged no candidate (is a live model tagged editor up," >&2
    echo "and does its server emit structured tool_calls?)" >&2
    echo "Hint: SMOKE_STAGE_DIRECT=1 stages the edit over HTTP instead." >&2
    exit 1
  fi
  echo "note: SMOKE_STAGE_DIRECT=1 — staging the edit over HTTP (model tool-call leg skipped)"
  post "/documents/$doc_id/edits" "$(python3 - "$block_id" <<'PY'
import json, sys
bid = sys.argv[1]
print(json.dumps({"blockId": bid, "text": "A quick brown fox leaps over a lazy dog."}))
PY
)" >/dev/null
  candidate_count=1
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
