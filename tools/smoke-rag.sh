#!/usr/bin/env bash
# Manual live smoke for the Phase C context engine: workspace -> multi-root
# corpus -> index -> metered auto-RAG turn with a persisted context snapshot ->
# idempotent eviction -> the ALLOWED_ROOTS refusal. NOT part of `go test`: it
# needs a running engine (default http://127.0.0.1:9100, override ENGINE_URL)
# with the control daemon up and the `nomic-embed` model provisioned (the
# retriever resolves it to embed queries and corpus chunks).
#
# It exercises the acceptance path end to end:
#   1. create-or-resume a workspace over a temp root
#   2. set a MULTI-ROOT corpus scope (a second root outside the workspace root)
#   3. POST /corpus/index and poll GET /corpus until every included doc indexes
#   4. POST /turn and assert the stream carries `rag` + `context` SSE events;
#      capture the turnId from the persisted snapshot
#   5. GET /turns/{id}/context and assert the snapshot is the engine's data
#   6. evict a document twice (idempotent, 204) and assert it is not retrievable
#   7. request a corpus root outside ALLOWED_ROOTS and assert the typed 403
#
# The engine owns ALLOWED_ROOTS (default $HOME or the configured projects
# root), so the temp vault MUST live under an allowed root — macOS `mktemp -d`
# defaults to /var/folders, which is outside it. Set SMOKE_BASE to an allowed
# directory (default $HOME) and the script creates its temp tree there.
#
# Usage: ENGINE_URL=http://127.0.0.1:9100 tools/smoke-rag.sh
#        SMOKE_BASE=/path/under/allowed/roots tools/smoke-rag.sh
set -euo pipefail

ENGINE_URL="${ENGINE_URL:-http://127.0.0.1:9100}"
SMOKE_BASE="${SMOKE_BASE:-$HOME}"

tmpdir="$(mktemp -d "${SMOKE_BASE%/}/texteditor-smoke-rag.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

WORKSPACE_ROOT="$tmpdir/workspace"
SHARED_ROOT="$tmpdir/shared"          # second corpus root, outside the workspace root
OUTSIDE_ROOT="$tmpdir/outside"        # never claimed; used for the refusal check
mkdir -p "$WORKSPACE_ROOT/sub" "$SHARED_ROOT" "$OUTSIDE_ROOT"

if ! curl -fsS -m 3 "$ENGINE_URL/health" >/dev/null 2>&1; then
  echo "FAIL: engine not reachable at $ENGINE_URL" >&2
  echo "Start it first (the control daemon must be up, see README 'Use the TUI'):" >&2
  echo "  cd server && go run ./cmd/texteditor --port 9100" >&2
  echo "or point the script at another engine: ENGINE_URL=http://host:port $0" >&2
  exit 1
fi

# Preflight: the daemon must know the embedding model the retriever resolves.
models="$(curl -fsS -m 5 "$ENGINE_URL/models" 2>/dev/null || true)"
if ! printf '%s' "$models" | grep -q '"nomic-embed"'; then
  echo "FAIL: 'nomic-embed' is not in the fleet projection (/models)." >&2
  echo "Provision/start it via the control daemon before running this smoke." >&2
  echo "projection: ${models:-<unreachable>}" >&2
  exit 1
fi

post() {
  local body="${2:-}"
  [ -n "$body" ] || body='{}'
  curl -fsS -X POST "$ENGINE_URL$1" -H 'content-type: application/json' -d "$body"
}
put() {
  local body="${2:-}"
  [ -n "$body" ] || body='{}'
  curl -fsS -X PUT "$ENGINE_URL$1" -H 'content-type: application/json' -d "$body"
}

echo "engine:        $ENGINE_URL"
echo "workspace:     $WORKSPACE_ROOT"
echo "shared root:   $SHARED_ROOT"

# Two markdown docs in the workspace root, one in a sibling root that is added
# to the corpus scope (workspace root never bounds retrievability, ADR-0049 §3).
printf '# Alpha\n\nalpha content about retrieval and the writing assistant.\n' >"$WORKSPACE_ROOT/a.md"
printf '# Beta\n\nbeta content lives in a subdirectory of the workspace root.\n' >"$WORKSPACE_ROOT/sub/b.md"
printf '# Shared\n\nshared literature in a sibling directory outside the workspace root.\n' >"$SHARED_ROOT/shared.md"
printf '# Outside\n\nthis directory is intentionally not corpus-scoped.\n' >"$OUTSIDE_ROOT/outside.md"

# 1. Workspace create-or-resume.
ws_json="$(post /workspaces "$(python3 - "$WORKSPACE_ROOT" <<'PY'
import json, sys
print(json.dumps({"root": sys.argv[1]}))
PY
)")"
ws_id="$(printf '%s' "$ws_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "workspace id:  $ws_id"

# 2. Multi-root corpus scope: the workspace root plus the sibling shared root.
put /corpus "$(python3 - "$ws_id" "$WORKSPACE_ROOT" "$SHARED_ROOT" <<'PY'
import json, sys
ws, root, shared = sys.argv[1:4]
print(json.dumps({"workspaceId": ws, "roots": [root, shared], "include": ["**/*.md"]}))
PY
)" >/dev/null
echo "PASS: PUT /corpus accepted a multi-root scope (workspace root + sibling)"

# 3. Async index + poll until the union of roots is indexed.
post /corpus/index "$(python3 - "$ws_id" <<'PY'
import json, sys
print(json.dumps({"workspaceId": sys.argv[1]}))
PY
)" >/dev/null

corpus_json=""
for _ in $(seq 1 40); do
  corpus_json="$(curl -fsS "$ENGINE_URL/corpus?workspaceId=$ws_id")"
  status="$(printf '%s' "$corpus_json" | python3 -c '
import json, sys
s = json.load(sys.stdin)
job = s.get("job") or {}
docs = s.get("documents") or []
state = job.get("state")
indexed = [d for d in docs if d.get("status") == "indexed"]
print("%s %d %d" % (state, len(indexed), len(docs)))
')"
  set -- $status
  job_state="${1:-}"; indexed_count="${2:-0}"
  if [ "$job_state" = "done" ]; then
    break
  fi
  sleep 0.5
done
if [ "${job_state:-}" != "done" ]; then
  echo "FAIL: corpus index job did not finish: $corpus_json" >&2
  exit 1
fi
if [ "$indexed_count" -lt 3 ]; then
  echo "FAIL: expected 3 indexed docs across both roots, got $indexed_count: $corpus_json" >&2
  exit 1
fi
echo "PASS: multi-root corpus indexed ($indexed_count docs from both roots)"

# 4. One turn: assert the stream carries both the `rag` and `context` SSE
#    events, and capture the turnId from the persisted snapshot.
turn_body="$(python3 - "$ws_id" "$WORKSPACE_ROOT" <<'PY'
import json, sys
ws, root = sys.argv[1:3]
print(json.dumps({
    "sessionId": "smoke-rag-session",
    "modeName": "drafter",
    "workspaceId": ws,
    "documentId": "",
    "userInput": "What does the shared literature say about retrieval?",
}))
PY
)"
stream_file="$tmpdir/stream.sse"
curl -fsS -N -X POST "$ENGINE_URL/turn" -H 'content-type: application/json' -d "$turn_body" >"$stream_file" || true

if ! grep -q '^event: rag$' "$stream_file"; then
  echo "FAIL: turn stream carried no 'rag' event (auto-RAG provenance visible)" >&2
  cat "$stream_file" >&2
  exit 1
fi
if ! grep -q '^event: context$' "$stream_file"; then
  echo "FAIL: turn stream carried no 'context' event (persisted snapshot)" >&2
  cat "$stream_file" >&2
  exit 1
fi
turn_id="$(python3 - "$stream_file" <<'PY'
import json, sys
turn = ""
for line in open(sys.argv[1]):
    if line.startswith("data: "):
        payload = line[len("data: "):].strip()
        try:
            obj = json.loads(payload)
        except Exception:
            continue
        if isinstance(obj, dict) and obj.get("turnId"):
            turn = obj["turnId"]
print(turn)
PY
)"
if [ -z "$turn_id" ]; then
  echo "FAIL: could not read the turnId from the context snapshot" >&2
  cat "$stream_file" >&2
  exit 1
fi
echo "PASS: turn emitted rag + context for turnId $turn_id"

# 5. Fetch the persisted snapshot by turn id.
snap="$(curl -fsS "$ENGINE_URL/turns/$turn_id/context")"
printf '%s' "$snap" | python3 -c '
import json, sys
s = json.load(sys.stdin)
assert s.get("turnId"), "snapshot has no turnId"
assert s.get("retrievalQuery"), "snapshot has no retrievalQuery"
assert isinstance(s.get("chunks"), list), "snapshot has no chunks"
assert isinstance(s.get("drops"), list), "snapshot has no drops"
assert isinstance(s.get("budget"), list), "snapshot has no budget"
print("snapshot: %d chunks, %d drops, %d budget rows" % (
    len(s["chunks"]), len(s["drops"]), len(s["budget"])))
'
echo "PASS: GET /turns/{id}/context returned the engine-owned snapshot"

# 6. Eviction is idempotent; the evicted document stays out of retrieval.
doc_id="$(printf '%s' "$corpus_json" | python3 -c '
import json, sys, os
s = json.load(sys.stdin)
want = None
for d in s.get("documents", []):
    if os.path.basename(d.get("path", "")) == "shared.md":
        want = d.get("id")
print(want or "")
')"
if [ -z "$doc_id" ]; then
  echo "FAIL: could not find the shared.md corpus doc id" >&2
  printf '%s\n' "$corpus_json" >&2
  exit 1
fi
for i in 1 2; do
  code="$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE \
    "$ENGINE_URL/corpus/documents/$doc_id?workspaceId=$ws_id")"
  if [ "$code" != "204" ]; then
    echo "FAIL: eviction #$i = HTTP $code, want 204 (idempotent)" >&2
    exit 1
  fi
done
after="$(curl -fsS "$ENGINE_URL/corpus?workspaceId=$ws_id")"
printf '%s' "$after" | python3 -c '
import json, sys
s = json.load(sys.stdin)
for d in s.get("documents", []):
    import os
    if os.path.basename(d.get("path", "")) == "shared.md":
        assert d.get("status") == "evicted", "shared.md status = %r, want evicted" % d.get("status")
'
echo "PASS: eviction is idempotent and the evicted document reports 'evicted'"

# 7. ALLOWED_ROOTS refusal: a corpus root outside the allowlist is a typed 403.
refusal="$tmpdir/refusal.json"
code="$(curl -sS -o "$refusal" -w '%{http_code}' -X PUT "$ENGINE_URL/corpus" \
  -H 'content-type: application/json' \
  -d "$(python3 - "$ws_id" "$OUTSIDE_ROOT" <<'PY'
import json, sys
ws, root = sys.argv[1:3]
print(json.dumps({"workspaceId": ws, "roots": [root]}))
PY
)")"
if [ "$code" != "403" ]; then
  echo "FAIL: outside-root corpus mutation = HTTP $code, want 403" >&2
  cat "$refusal" >&2
  exit 1
fi
if ! grep -q 'path-outside-allowed-roots' "$refusal"; then
  echo "FAIL: 403 body missing the typed path-outside-allowed-roots error" >&2
  cat "$refusal" >&2
  exit 1
fi
echo "PASS: ALLOWED_ROOTS refusal is typed (path-outside-allowed-roots)"

echo "ALL PASS: smoke-rag"
