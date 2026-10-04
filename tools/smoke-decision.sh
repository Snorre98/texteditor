#!/usr/bin/env bash
# Manual live smoke for the Phase F Laya decision layer (ADR-0053): the global
# policy surface, the planner (retrieve-or-not/thinking/breadth), the per-chunk
# gate, the typed snapshot record, the metered `decision` row, pins bypassing
# the gate, and the labeled fail-open when Laya is down. NOT part of `go test`:
# it needs a running engine (default http://127.0.0.1:9100, override ENGINE_URL)
# with the control daemon up, `nomic-embed` provisioned, and the Laya service
# (`laya`) reachable.
#
# It exercises the exact path a corpus turn takes once the tray toggle is on:
#   1. GET /decision -> the global policy block
#   2. workspace -> corpus -> index (a doc the query retrieves)
#   3. a turn with a per-turn ContextPolicy.decision=on -> rag + context
#   4. GET /turns/{id}/context -> decision.planner + decision.gate.chunks +
#      survivors + labeled gate drops + rag is pre-gate
#   5. GET /sessions/{id}/meter -> a `decision` component row
#   6. PUT /sessions/{id}/context decision=on -> a second turn is gated
#   7. pins bypass the gate (the pinned chunk survives and is labeled)
#   8. SMOKE_LAYADOWN=1 -> stop laya, rerun, assert decision.degraded + keep-all
#
# The temp vault MUST live under ALLOWED_ROOTS (macOS `mktemp` defaults to
# /var/folders, outside it). Set SMOKE_BASE to an allowed directory (default
# $HOME).
#
# Usage: ENGINE_URL=http://127.0.0.1:9100 tools/smoke-decision.sh
#        SMOKE_BASE=/path/under/allowed/roots tools/smoke-decision.sh
#        SMOKE_LAYADOWN=1 tools/smoke-decision.sh   # exercises the degraded path
set -euo pipefail

ENGINE_URL="${ENGINE_URL:-http://127.0.0.1:9100}"
SMOKE_BASE="${SMOKE_BASE:-$HOME}"
SERVE_LAYA="${SERVE_LAYA:-/Users/snorresaether/Projects/macos-dev-config/tools/serve-laya.sh}"
SMOKE_LAYADOWN="${SMOKE_LAYADOWN:-0}"

tmpdir="$(mktemp -d "${SMOKE_BASE%/}/texteditor-smoke-decision.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

WORKSPACE_ROOT="$tmpdir/workspace"
mkdir -p "$WORKSPACE_ROOT"

if ! curl -fsS -m 3 "$ENGINE_URL/health" >/dev/null 2>&1; then
  echo "FAIL: engine not reachable at $ENGINE_URL" >&2
  echo "  cd server && go run ./cmd/texteditor --port 9100" >&2
  exit 1
fi

models="$(curl -fsS -m 5 "$ENGINE_URL/models" 2>/dev/null || true)"
if ! printf '%s' "$models" | grep -q '"nomic-embed"'; then
  echo "FAIL: 'nomic-embed' is not in the fleet projection (/models)." >&2
  echo "projection: ${models:-<unreachable>}" >&2
  exit 1
fi

decision_policy="$(curl -fsS -m 5 "$ENGINE_URL/decision" 2>/dev/null || true)"
if [ -z "$decision_policy" ]; then
  echo "FAIL: GET /decision returned nothing (engine older than Phase F?)" >&2
  exit 1
fi
decision_model="$(printf '%s' "$decision_policy" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("model",""))')"
if [ -z "$decision_model" ]; then
  echo "FAIL: GET /decision has no model" >&2
  printf '%s\n' "$decision_policy" >&2
  exit 1
fi
if ! printf '%s' "$models" | grep -q "\"$decision_model\""; then
  echo "FAIL: decision model '$decision_model' is not in the fleet projection (/models)." >&2
  echo "Start it: serve-laya.sh start (or provision '$decision_model' via the control daemon)." >&2
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

# turn_body USER_INPUT EXTRA_JSON -> a Task JSON with the workspace id.
turn_body() {
  python3 - "$ws_id" "$1" "${2:-{}}" <<'PY'
import json, sys
ws, user, extra = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
body = {"sessionId": "smoke-decision-session", "modeName": "drafter",
        "workspaceId": ws, "documentId": "", "userInput": user}
body.update(extra)
print(json.dumps(body))
PY
}

# run_turn BODY STREAM_FILE -> writes the SSE stream, echoes the turnId.
run_turn() {
  curl -fsS -N -X POST "$ENGINE_URL/turn" -H 'content-type: application/json' -d "$1" >"$2" || true
  python3 - "$2" <<'PY'
import json, sys
turn = ""
for line in open(sys.argv[1]):
    if line.startswith("data: "):
        try:
            obj = json.loads(line[len("data: "):].strip())
        except Exception:
            continue
        if isinstance(obj, dict) and obj.get("turnId"):
            turn = obj["turnId"]
print(turn)
PY
}

echo "engine:   $ENGINE_URL"
echo "vault:    $WORKSPACE_ROOT"
echo "decision: model=$decision_model enabled=$(printf '%s' "$decision_policy" | python3 -c 'import json,sys; print(json.load(sys.stdin)["enabled"])')"

# 1. Global policy surface.
printf '%s' "$decision_policy" | python3 -c '
import json, sys
p = json.load(sys.stdin)
for k in ("enabled","model","gateThreshold","maxCandidates","maxHistoryTurns","breadthTopK","timeoutMs"):
    assert k in p, "missing %s" % k
assert set(p["breadthTopK"]) >= {"none","few","many"}, p["breadthTopK"]
print("PASS: GET /decision returns the global policy (tau=%.2f, candidates=%d)" % (p["gateThreshold"], p["maxCandidates"]))
'

# 2. Corpus: one doc the query clearly retrieves.
printf '# Retrieval gating\n\nThe decision gate keeps only the passages that are directly relevant to the writing request, dropping the rest and recording every drop as a labeled gate drop.\n' >"$WORKSPACE_ROOT/gating.md"
ws_json="$(post /workspaces "$(python3 - "$WORKSPACE_ROOT" <<'PY'
import json, sys
print(json.dumps({"root": sys.argv[1]}))
PY
)")"
ws_id="$(printf '%s' "$ws_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
put /corpus "$(python3 - "$ws_id" "$WORKSPACE_ROOT" <<'PY'
import json, sys
ws, root = sys.argv[1:3]
print(json.dumps({"workspaceId": ws, "roots": [root], "include": ["**/*.md"]}))
PY
)" >/dev/null
post /corpus/index "$(python3 - "$ws_id" <<'PY'
import json, sys
print(json.dumps({"workspaceId": sys.argv[1]}))
PY
)" >/dev/null
for _ in $(seq 1 60); do
  state="$(curl -fsS "$ENGINE_URL/corpus?workspaceId=$ws_id" | python3 -c 'import json,sys; print((json.load(sys.stdin).get("job") or {}).get("state") or "")')"
  [ "$state" = "done" ] && break
  [ "$state" = "error" ] && { echo "FAIL: corpus index errored" >&2; exit 1; }
  sleep 0.5
done
[ "$state" = "done" ] || { echo "FAIL: corpus index did not finish" >&2; exit 1; }
echo "PASS: corpus indexed"

QUERY="Which passages are directly relevant to the request, and what happens to the passages the decision gate drops?"

# 3. Turn with a per-turn ContextPolicy.decision=on override.
stream1="$tmpdir/stream1.sse"
turn1="$(run_turn "$(turn_body "$QUERY" '{"context":{"decision":"on"}}')" "$stream1")"
grep -q '^event: rag$' "$stream1" || { echo "FAIL: no rag event" >&2; cat "$stream1" >&2; exit 1; }
grep -q '^event: context$' "$stream1" || { echo "FAIL: no context event" >&2; cat "$stream1" >&2; exit 1; }
[ -n "$turn1" ] || { echo "FAIL: no turnId" >&2; cat "$stream1" >&2; exit 1; }
echo "PASS: turn ran with a per-turn decision=on (turnId $turn1)"

# 4. Typed decision record in the persisted snapshot.
snap1="$tmpdir/snap1.json"
curl -fsS "$ENGINE_URL/turns/$turn1/context" >"$snap1"
rag_chunks="$(python3 - "$stream1" <<'PY'
import json, sys
n = 0
for line in open(sys.argv[1]):
    if line.startswith("data: "):
        try:
            obj = json.loads(line[len("data: "):].strip())
        except Exception:
            continue
        if isinstance(obj, dict) and "chunks" in obj and "ok" in obj:
            n = len(obj["chunks"])
print(n)
PY
)"
SMOKE_LAYADOWN="$SMOKE_LAYADOWN" python3 - "$snap1" "$rag_chunks" <<'PY'
import json, os, sys
snap = json.load(open(sys.argv[1]))
rag = int(sys.argv[2])
deg = os.environ.get("SMOKE_LAYADOWN") == "1"
d = snap.get("decision")
assert d is not None, "snapshot has no decision record"
assert d.get("enabled") is True, d
if deg:
    assert d.get("degraded") is True, "expected degraded, got %r" % d
    assert d.get("reason"), "degraded record has no reason"
    assert len(snap.get("chunks", [])) >= 1, "degraded gate must keep chunks"
    print("PASS: decision degraded + labeled (%s); %d chunks kept" % (d["reason"], len(snap["chunks"])))
    sys.exit(0)
assert d.get("degraded") is False, "unexpected degrade: %r" % d.get("reason")
p = d.get("planner")
assert p is not None, "no planner record"
assert p.get("retrieve") is True, "planner did not choose retrieval: %r" % p
g = d.get("gate")
assert g is not None, "no gate record"
chunks = g.get("chunks") or []
assert chunks, "gate recorded no chunk decisions"
assert all("keep" in c and "chunkKey" in c for c in chunks), chunks
kept = [c for c in chunks if c["keep"]]
# survivors == pinned (0) + kept auto
survivors = [c for c in snap.get("chunks", []) if not c.get("humanOverride")]
assert len(survivors) == len(kept), "survivors %d != kept %d" % (len(survivors), len(kept))
# rag event is pre-gate (>= survivors)
assert rag >= len(survivors), "rag %d < survivors %d (not pre-gate)" % (rag, len(survivors))
# every gate drop is labeled
gate_drops = [x for x in snap.get("drops", []) if x.get("reason") == "gate" and x.get("component") == "rag"]
dropped = len(chunks) - len(kept)
assert sum(x.get("count", 0) for x in gate_drops) == dropped, "gate drops %r != dropped %d" % (gate_drops, dropped)
print("PASS: snapshot decision.planner + gate (%d candidates, %d kept, %d dropped) + rag pre-gate" % (len(chunks), len(kept), dropped))
PY

# 5. Metered `decision` row.
meter="$(curl -fsS "$ENGINE_URL/sessions/smoke-decision-session/meter")"
printf '%s' "$meter" | python3 -c '
import json, sys
m = json.load(sys.stdin)
comps = {c["component"]: c for c in m.get("components", [])}
assert "decision" in comps, "no decision component: %r" % list(comps)
c = comps["decision"]
assert (c.get("promptTokens", 0) + c.get("completionTokens", 0)) > 0, c
print("PASS: meter has a decision row (prompt=%d completion=%d)" % (c.get("promptTokens", 0), c.get("completionTokens", 0)))
'

if [ "$SMOKE_LAYADOWN" = "1" ]; then
  echo "SMOKE_LAYADOWN=1: stopping laya and rerunning to exercise the degraded path"
  "$SERVE_LAYA" stop >/dev/null 2>&1 || true
  stream3="$tmpdir/stream3.sse"
  turn3="$(run_turn "$(turn_body "$QUERY" '{"context":{"decision":"on"}}')" "$stream3")"
  [ -n "$turn3" ] || { echo "FAIL: degraded turn produced no turnId" >&2; cat "$stream3" >&2; exit 1; }
  curl -fsS "$ENGINE_URL/turns/$turn3/context" >"$tmpdir/snap3.json"
  python3 - "$tmpdir/snap3.json" <<'PY'
import json, sys
snap = json.load(open(sys.argv[1]))
d = snap.get("decision") or {}
assert d.get("degraded") is True, "expected degraded after stopping laya: %r" % d
assert d.get("reason") in ("unreachable","timeout","protocol"), d
print("PASS: turn proceeded fail-open with decision.degraded=%s" % d["reason"])
PY
  "$SERVE_LAYA" start >/dev/null 2>&1 || true
  echo "ALL PASS: smoke-decision (degraded path)"
  exit 0
fi

# 6. Session-policy path: PUT decision=on, then a turn with no per-turn override.
put "/sessions/smoke-decision-session/context" '{"decision":"on"}' >/dev/null
stream2="$tmpdir/stream2.sse"
turn2="$(run_turn "$(turn_body "$QUERY")" "$stream2")"
[ -n "$turn2" ] || { echo "FAIL: session-policy turn produced no turnId" >&2; cat "$stream2" >&2; exit 1; }
curl -fsS "$ENGINE_URL/turns/$turn2/context" >"$tmpdir/snap2.json"
python3 - "$tmpdir/snap2.json" <<'PY'
import json, sys
snap = json.load(open(sys.argv[1]))
d = snap.get("decision") or {}
assert d.get("enabled") is True, "session decision=on did not enable the layer: %r" % d
print("PASS: session ContextPolicy.decision=on enabled the layer")
PY

# 7. Pins bypass the gate: pin the first candidate and rerun.
pin_key="$(python3 - "$snap1" <<'PY'
import json, sys
snap = json.load(open(sys.argv[1]))
for c in snap.get("decision", {}).get("gate", {}).get("chunks", []):
    print(c.get("chunkKey", ""))
    break
PY
)"
if [ -n "$pin_key" ]; then
  put "/sessions/smoke-decision-session/context" "$(python3 - "$pin_key" <<'PY'
import json, sys
print(json.dumps({"decision": "on", "pinned": [{"chunkKey": sys.argv[1]}]}))
PY
)" >/dev/null
  stream4="$tmpdir/stream4.sse"
  turn4="$(run_turn "$(turn_body "$QUERY")" "$stream4")"
  curl -fsS "$ENGINE_URL/turns/$turn4/context" >"$tmpdir/snap4.json"
  python3 - "$tmpdir/snap4.json" <<'PY'
import json, sys
snap = json.load(open(sys.argv[1]))
pins = [c for c in snap.get("chunks", []) if c.get("humanOverride")]
assert pins, "the pinned chunk did not survive the gate"
print("PASS: pinned chunk bypassed the gate (%d pin)" % len(pins))
PY
else
  echo "WARN: no gate candidate to pin; skipping the pins-bypass step" >&2
fi

echo "ALL PASS: smoke-decision"
