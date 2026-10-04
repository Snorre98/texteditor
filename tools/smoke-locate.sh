#!/usr/bin/env bash
# Manual live smoke for Phase D `/locate` chunk anchoring (ADR-0048). NOT part
# of `go test`: it needs a running engine (default http://127.0.0.1:9100,
# override ENGINE_URL) with the control daemon and a live model tagged `editor`
# (the only shipped preset that dispatches edit_markdown — the anchored turn
# needs the model to call the tool; the loop forces the blockId/baseHash).
#
# It exercises the acceptance path end to end:
#   1. exact paste -> locate event (resolved, block id, turnId) BEFORE any token
#      -> the model stages an anchored edit_markdown candidate -> accept -> the
#      file changes on disk
#   2. fuzzy paste -> locate event (ambiguous) -> POST /turns/{id}/locate with a
#      candidate chunkKey -> the turn resumes and completes
#   3. unrelated paste -> locate event (not-found) -> the turn proceeds as plain
#      chat (done)
#   4. no model call for locate: the locate event precedes every token
#
# The temp vault MUST live under ALLOWED_ROOTS (macOS mktemp defaults to
# /var/folders, outside it). Set SMOKE_BASE to an allowed directory (default
# $HOME).
#
# Usage: ENGINE_URL=http://127.0.0.1:9100 tools/smoke-locate.sh
#        SMOKE_BASE=/path/under/allowed/roots tools/smoke-locate.sh
set -euo pipefail

ENGINE_URL="${ENGINE_URL:-http://127.0.0.1:9100}"
SMOKE_BASE="${SMOKE_BASE:-$HOME}"

tmpdir="$(mktemp -d "${SMOKE_BASE%/}/texteditor-smoke-locate.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

NOTE="$tmpdir/note.md"
PARA="The quick brown fox jumps over the lazy dog in the quiet meadow."
FUZZY="The quick brown fox leaps over the lazy dog in the quiet meadow."
OTHER="Quantum chromodynamics describes the strong nuclear force between quarks."

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

printf '%s\n' "$PARA" >"$NOTE"
echo "engine: $ENGINE_URL"
echo "note:   $NOTE"

doc_id="$(post /documents "{\"path\":\"$NOTE\"}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
blocks_json="$(curl -fsS "$ENGINE_URL/documents/$doc_id/blocks")"
block_id="$(printf '%s' "$blocks_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')"
echo "document: $doc_id  anchor block: $block_id"

# turn_body <userInput> -> a JSON Task body.
turn_body() {
  python3 - "$doc_id" "$1" <<'PY'
import json, sys
doc, text = sys.argv[1:3]
print(json.dumps({
    "sessionId": f"smoke-locate-{doc}",
    "modeName": "editor",
    "documentId": doc,
    "userInput": text,
}))
PY
}

# locate_field <stream-file> <python-expression over obj> -> prints the value.
locate_field() {
  python3 - "$1" "$2" <<'PY'
import json, sys
stream, expr = sys.argv[1:3]
obj = None
for line in open(stream):
    if line.startswith("data: "):
        try:
            cand = json.loads(line[len("data: "):].strip())
        except Exception:
            continue
        if isinstance(cand, dict) and cand.get("status"):
            obj = cand
            break
if obj is None:
    print("")
    sys.exit(0)
print(eval(expr, {"obj": obj, "json": json}))  # noqa: S307 (smoke-local, trusted input)
PY
}

# 1. Exact paste resolves and anchors; the locate event precedes every token.
exact_stream="$tmpdir/exact.sse"
curl -fsS -N -X POST "$ENGINE_URL/turn" -H 'content-type: application/json' \
  -d "$(turn_body "/locate
$PARA")" >"$exact_stream" || true

status="$(locate_field "$exact_stream" 'obj.get("status")')"
got_block="$(locate_field "$exact_stream" 'obj.get("blockId")')"
turn_id="$(locate_field "$exact_stream" 'obj.get("turnId")')"
if [ "$status" != "resolved" ] || [ "$got_block" != "$block_id" ]; then
  echo "FAIL: exact locate = status=$status block=$got_block (want resolved/$block_id)" >&2
  cat "$exact_stream" >&2
  exit 1
fi
if [ -z "$turn_id" ]; then
  echo "FAIL: locate event carried no turnId (picker correlation)" >&2
  cat "$exact_stream" >&2
  exit 1
fi
loc_line="$(grep -n '^event: locate$' "$exact_stream" | head -1 | cut -d: -f1)"
tok_line="$(grep -n '^event: token$' "$exact_stream" | head -1 | cut -d: -f1)"
if [ -z "$loc_line" ] || { [ -n "$tok_line" ] && [ "$loc_line" -gt "$tok_line" ]; }; then
  echo "FAIL: locate event did not precede the model answer (locate=$loc_line token=$tok_line)" >&2
  exit 1
fi
echo "PASS: exact paste resolved to block $block_id before any token (turnId $turn_id)"

cand_count="$(curl -fsS "$ENGINE_URL/documents/$doc_id/blocks/$block_id/candidates" \
  | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"
if [ "$cand_count" -eq 0 ]; then
  echo "FAIL: the anchored turn staged no candidate (is a live model tagged editor up," >&2
  echo "and does its server emit structured tool_calls?)" >&2
  cat "$exact_stream" >&2
  exit 1
fi
post "/documents/$doc_id/commits" '{}' >/dev/null
if [ "$(cat "$NOTE")" = "$PARA" ]; then
  echo "FAIL: accept did not change the file on disk" >&2
  exit 1
fi
echo "PASS: anchored candidate accepted and written through to disk"

# 2. Fuzzy paste is ambiguous; the turn waits and resumes on the picker choice.
printf '%s\n' "$PARA" >"$NOTE"   # reset (the exact leg rewrote it)
post "/documents/$doc_id/commits" '{"overwrite":true}' >/dev/null || true
fuzzy_stream="$tmpdir/fuzzy.sse"
curl -fsS -N -X POST "$ENGINE_URL/turn" -H 'content-type: application/json' \
  -d "$(turn_body "/locate
$FUZZY")" >"$fuzzy_stream" 2>/dev/null &
fuzzy_pid=$!

# Wait for the locate event, then answer the picker while the stream is open.
picker_key=""
fuzzy_turn=""
for _ in $(seq 1 60); do
  if [ -s "$fuzzy_stream" ] && grep -q '^event: locate$' "$fuzzy_stream"; then
    fuzzy_status="$(locate_field "$fuzzy_stream" 'obj.get("status")')"
    fuzzy_turn="$(locate_field "$fuzzy_stream" 'obj.get("turnId")')"
    picker_key="$(locate_field "$fuzzy_stream" 'obj["candidates"][0]["chunkKey"] if obj.get("candidates") else ""')"
    if [ "$fuzzy_status" = "ambiguous" ] && [ -n "$picker_key" ]; then
      break
    fi
  fi
  sleep 0.25
done
if [ "$fuzzy_status" != "ambiguous" ] || [ -z "$picker_key" ] || [ -z "$fuzzy_turn" ]; then
  echo "FAIL: fuzzy paste did not yield an ambiguous locate with candidates" >&2
  cat "$fuzzy_stream" >&2 || true
  kill "$fuzzy_pid" 2>/dev/null || true
  exit 1
fi
post "/turns/$fuzzy_turn/locate" "$(python3 - "$picker_key" <<'PY'
import json, sys
print(json.dumps({"chunkKey": sys.argv[1]}))
PY
)" >/dev/null
wait "$fuzzy_pid" || true
if ! grep -q '^event: done$' "$fuzzy_stream"; then
  echo "FAIL: fuzzy turn did not complete after the picker choice" >&2
  cat "$fuzzy_stream" >&2
  exit 1
fi
echo "PASS: fuzzy paste waited for the picker and resumed on choice $picker_key"

# 3. Not found degrades to plain chat with a labeled not-found locate event.
nf_stream="$tmpdir/notfound.sse"
curl -fsS -N -X POST "$ENGINE_URL/turn" -H 'content-type: application/json' \
  -d "$(turn_body "/locate
$OTHER")" >"$nf_stream" || true
nf_status="$(locate_field "$nf_stream" 'obj.get("status")')"
if [ "$nf_status" != "not-found" ]; then
  echo "FAIL: unrelated paste = locate status $nf_status, want not-found" >&2
  cat "$nf_stream" >&2
  exit 1
fi
if ! grep -q '^event: done$' "$nf_stream"; then
  echo "FAIL: not-found turn did not proceed as plain chat" >&2
  cat "$nf_stream" >&2
  exit 1
fi
echo "PASS: not-found locate degraded to plain chat (labeled not-found)"

echo "ALL PASS: smoke-locate"
