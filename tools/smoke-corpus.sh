#!/usr/bin/env bash
# Manual live smoke for the Phase E2c corpus tree surface over the Phase C
# corpus contract: workspace -> scope -> index -> per-document status (incl.
# stale) -> idempotent eviction -> typed ALLOWED_ROOTS refusal. NOT part of
# `go test`: it needs a running engine (default http://127.0.0.1:9100, override
# ENGINE_URL) with the control daemon up and `nomic-embed` provisioned (the
# retriever resolves it to embed corpus chunks).
#
# It exercises the exact routes the Ratatui TUI corpus overlay (Ctrl+K) calls:
#   1. create-or-resume a workspace over a temp root
#   2. PUT /corpus sets the scope; the reconcile job completes
#   3. POST /corpus/index (index/rebuild) and poll GET /corpus to `done`
#   4. GET /corpus reports per-document status + chunk counts
#   5. an external file change makes the document report `stale`
#   6. DELETE /corpus/documents/{id} is idempotent and reports `evicted`
#   7. a corpus root outside ALLOWED_ROOTS is the typed 403
#
# The engine owns ALLOWED_ROOTS (default $HOME or the configured projects
# root), so the temp vault MUST live under an allowed root — macOS `mktemp -d`
# defaults to /var/folders, which is outside it. Set SMOKE_BASE to an allowed
# directory (default $HOME) and the script creates its temp tree there.
#
# Usage: ENGINE_URL=http://127.0.0.1:9100 tools/smoke-corpus.sh
#        SMOKE_BASE=/path/under/allowed/roots tools/smoke-corpus.sh
set -euo pipefail

ENGINE_URL="${ENGINE_URL:-http://127.0.0.1:9100}"
SMOKE_BASE="${SMOKE_BASE:-$HOME}"

tmpdir="$(mktemp -d "${SMOKE_BASE%/}/texteditor-smoke-corpus.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

WORKSPACE_ROOT="$tmpdir/workspace"
OUTSIDE_ROOT="$tmpdir/outside"        # never claimed; used for the refusal check
mkdir -p "$WORKSPACE_ROOT/sub" "$OUTSIDE_ROOT"

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

# poll_corpus WS_ID: GET /corpus until the latest job is `done`, then echo the state.
poll_corpus() {
  local ws="$1" corpus_json=""
  for _ in $(seq 1 60); do
    corpus_json="$(curl -fsS "$ENGINE_URL/corpus?workspaceId=$ws")"
    local state
    state="$(printf '%s' "$corpus_json" | python3 -c 'import json,sys; print((json.load(sys.stdin).get("job") or {}).get("state") or "")')"
    if [ "$state" = "done" ]; then
      printf '%s' "$corpus_json"
      return 0
    fi
    if [ "$state" = "error" ]; then
      echo "FAIL: corpus job errored: $corpus_json" >&2
      return 1
    fi
    sleep 0.5
  done
  echo "FAIL: corpus job did not finish: $corpus_json" >&2
  return 1
}

echo "engine:        $ENGINE_URL"
echo "workspace:     $WORKSPACE_ROOT"

printf '# Alpha\n\nalpha content about retrieval and the writing assistant.\n' >"$WORKSPACE_ROOT/a.md"
printf '# Beta\n\nbeta content lives in a subdirectory of the workspace root.\n' >"$WORKSPACE_ROOT/sub/b.md"
printf '# Outside\n\nthis directory is intentionally not corpus-scoped.\n' >"$OUTSIDE_ROOT/outside.md"

# 1. Workspace create-or-resume.
ws_json="$(post /workspaces "$(python3 - "$WORKSPACE_ROOT" <<'PY'
import json, sys
print(json.dumps({"root": sys.argv[1]}))
PY
)")"
ws_id="$(printf '%s' "$ws_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
echo "workspace id:  $ws_id"

# 2. Scope (roots = workspace root; defaults include **/*.md, hidden excluded).
put /corpus "$(python3 - "$ws_id" "$WORKSPACE_ROOT" <<'PY'
import json, sys
ws, root = sys.argv[1:3]
print(json.dumps({"workspaceId": ws, "roots": [root], "include": ["**/*.md"]}))
PY
)" >/dev/null
poll_corpus "$ws_id" >/dev/null
echo "PASS: PUT /corpus set the scope and the reconcile job completed"

# 3. Index/rebuild (the overlay's `i` key) + poll.
post /corpus/index "$(python3 - "$ws_id" <<'PY'
import json, sys
print(json.dumps({"workspaceId": sys.argv[1]}))
PY
)" >/dev/null
corpus_json="$(poll_corpus "$ws_id")"

# 4. Per-document status + chunk counts (the overlay's rows).
printf '%s' "$corpus_json" | python3 -c '
import json, sys
s = json.load(sys.stdin)
docs = s.get("documents") or []
indexed = [d for d in docs if d.get("status") == "indexed"]
assert len(indexed) >= 2, "expected >=2 indexed docs, got %d" % len(indexed)
assert all(d.get("chunkCount", 0) >= 1 for d in indexed), "indexed doc missing chunkCount"
print("indexed: %d docs" % len(indexed))
'
echo "PASS: GET /corpus reports per-document status + chunk counts"

# 5. External change -> stale.
printf '# Alpha\n\nalpha content edited OUTSIDE the engine after indexing.\n' >"$WORKSPACE_ROOT/a.md"
stale_json="$(curl -fsS "$ENGINE_URL/corpus?workspaceId=$ws_id")"
printf '%s' "$stale_json" | python3 -c '
import json, sys, os
s = json.load(sys.stdin)
hit = [d for d in (s.get("documents") or []) if os.path.basename(d.get("path", "")) == "a.md"]
assert hit, "a.md absent from corpus status"
assert hit[0].get("status") == "stale", "a.md status = %r, want stale" % hit[0].get("status")
'
echo "PASS: an externally changed document reports 'stale'"

# 6. Idempotent eviction (the overlay's `e` key).
doc_id="$(printf '%s' "$corpus_json" | python3 -c '
import json, sys, os
s = json.load(sys.stdin)
for d in s.get("documents", []):
    if os.path.basename(d.get("path", "")) == "sub/b.md":
        print(d.get("id") or "")
        break
')"
if [ -z "$doc_id" ]; then
  echo "FAIL: could not find the sub/b.md corpus doc id" >&2
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
import json, sys, os
s = json.load(sys.stdin)
for d in s.get("documents", []):
    if os.path.basename(d.get("path", "")) == "b.md":
        assert d.get("status") == "evicted", "b.md status = %r, want evicted" % d.get("status")
'
echo "PASS: eviction is idempotent and the evicted document reports 'evicted'"

# 7. ALLOWED_ROOTS refusal on scope (the typed path-outside-allowed-roots).
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

echo "ALL PASS: smoke-corpus"
