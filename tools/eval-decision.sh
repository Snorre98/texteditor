#!/usr/bin/env bash
# F4 decision-layer evaluation harness (ADR-0053). NOT part of `go test`: it
# needs a running engine (default http://127.0.0.1:9100, override ENGINE_URL)
# with the control daemon up, `nomic-embed` provisioned, the Laya service
# reachable, and a model tagged for the mode it runs (default `drafter`).
#
# It runs the golden fixture (server/testdata/decision-golden/) twice per query
# — once with the decision layer off (baseline) and once on (gated) — and prints
# recall / injected-token / keep-rate metrics. The τ and breadthTopK sweeps are
# config-driven: this harness measures the CURRENT engine config, so to sweep,
# edit config/pipeline.json, rebuild/restart the engine, and rerun with a label:
#
#   SMOKE_LABEL="tau=0.4,breadth=few" tools/eval-decision.sh
#
# Use a real thesis-scale corpus by pointing SMOKE_CORPUS at the vault and
# SMOKE_QUERIES at your labeled queries (same JSONL shape).
#
# Usage:
#   tools/eval-decision.sh
#   SMOKE_CORPUS=~/thesis SMOKE_QUERIES=~/thesis/queries.jsonl tools/eval-decision.sh
#   SMOKE_LABEL="tau=0.4,breadth=few" tools/eval-decision.sh --record
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENGINE_URL="${ENGINE_URL:-http://127.0.0.1:9100}"
SMOKE_BASE="${SMOKE_BASE:-$HOME}"
SMOKE_CORPUS="${SMOKE_CORPUS:-$REPO_ROOT/server/testdata/decision-golden/corpus}"
SMOKE_QUERIES="${SMOKE_QUERIES:-$REPO_ROOT/server/testdata/decision-golden/queries.jsonl}"
SMOKE_MODE="${SMOKE_MODE:-drafter}"
SMOKE_LABEL="${SMOKE_LABEL:-current config}"
RECORD=0
[ "${1:-}" = "--record" ] && RECORD=1

tmpdir="$(mktemp -d "${SMOKE_BASE%/}/texteditor-eval-decision.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

if ! curl -fsS -m 3 "$ENGINE_URL/health" >/dev/null 2>&1; then
  echo "FAIL: engine not reachable at $ENGINE_URL" >&2
  exit 1
fi
models="$(curl -fsS -m 5 "$ENGINE_URL/models" 2>/dev/null || true)"
printf '%s' "$models" | grep -q '"nomic-embed"' || { echo "FAIL: nomic-embed missing from /models" >&2; exit 1; }
decision_model="$(curl -fsS -m 5 "$ENGINE_URL/decision" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("model",""))')"
printf '%s' "$models" | grep -q "\"$decision_model\"" || { echo "FAIL: decision model '$decision_model' missing from /models" >&2; exit 1; }

post() { curl -fsS -X POST "$ENGINE_URL$1" -H 'content-type: application/json' -d "${2:-{\}}"; }
put()  { curl -fsS -X PUT  "$ENGINE_URL$1" -H 'content-type: application/json' -d "${2:-{\}}"; }

# Workspace over the corpus root (its parent must be under ALLOWED_ROOTS).
ws_json="$(post /workspaces "$(python3 - "$SMOKE_CORPUS" <<'PY'
import json, sys, os
print(json.dumps({"root": os.path.dirname(os.path.abspath(sys.argv[1]))}))
PY
)")"
ws_id="$(printf '%s' "$ws_json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
put /corpus "$(python3 - "$ws_id" "$SMOKE_CORPUS" <<'PY'
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
for _ in $(seq 1 90); do
  state="$(curl -fsS "$ENGINE_URL/corpus?workspaceId=$ws_id" | python3 -c 'import json,sys; print((json.load(sys.stdin).get("job") or {}).get("state") or "")')"
  [ "$state" = "done" ] && break
  [ "$state" = "error" ] && { echo "FAIL: corpus index errored" >&2; exit 1; }
  sleep 0.5
done
[ "$state" = "done" ] || { echo "FAIL: corpus index did not finish" >&2; exit 1; }

echo "engine:   $ENGINE_URL"
echo "corpus:   $SMOKE_CORPUS"
echo "queries:  $SMOKE_QUERIES"
echo "label:    $SMOKE_LABEL"
echo

# Run one turn; write the SSE stream and the snapshot. Echo the turnId.
run_turn() { # user_input decision_gated stream_file snap_file
  local body stream="$3" snap="$4"
  body="$(python3 - "$ws_id" "$1" "$2" "$SMOKE_MODE" <<'PY'
import json, sys
ws, user, on, mode = sys.argv[1], sys.argv[2], sys.argv[3] == "on", sys.argv[4]
print(json.dumps({"sessionId": "eval-decision-session", "modeName": mode, "workspaceId": ws,
                  "documentId": "", "userInput": user, "context": {"decision": "planner+gate" if on else "off"}}))
PY
)"
  curl -fsS -N -X POST "$ENGINE_URL/turn" -H 'content-type: application/json' -d "$body" >"$stream" || true
  local turn
  turn="$(python3 - "$stream" <<'PY'
import json, sys
for line in open(sys.argv[1]):
    if line.startswith("data: "):
        try:
            o = json.loads(line[len("data: "):].strip())
        except Exception:
            continue
        if isinstance(o, dict) and o.get("turnId"):
            print(o["turnId"]); break
PY
)"
  [ -n "$turn" ] && curl -fsS "$ENGINE_URL/turns/$turn/context" >"$snap" || true
}

# Per query: baseline (off) then gated (on).
n=0
while IFS= read -r line; do
  [ -n "$line" ] || continue
  n=$((n + 1))
  q="$(printf '%s' "$line" | python3 -c 'import json,sys; print(json.load(sys.stdin)["query"])')"
  run_turn "$q" off "$tmpdir/q$n.off.sse" "$tmpdir/q$n.off.json"
  run_turn "$q" on  "$tmpdir/q$n.on.sse"  "$tmpdir/q$n.on.json"
  printf '  ran query %d\n' "$n"
done <"$SMOKE_QUERIES"

echo
python3 - "$SMOKE_QUERIES" "$tmpdir" "$n" "$SMOKE_LABEL" "$RECORD" "$REPO_ROOT" <<'PY'
import json, os, sys, statistics, datetime

queries_path, tmp, n, label, record, repo = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4], sys.argv[5] == "1", sys.argv[6]
queries = [json.loads(l) for l in open(queries_path) if l.strip()]

def rag_chunks(sse_path):
    out = []
    for line in open(sse_path):
        if line.startswith("data: "):
            try:
                o = json.loads(line[len("data: "):].strip())
            except Exception:
                continue
            if isinstance(o, dict) and "ok" in o and "chunks" in o:
                out = o["chunks"]
    return out

def snap(path):
    try:
        return json.load(open(path))
    except Exception:
        return {}

def matches(ref, chunks):
    # ref = "file.md#Heading"
    if "#" not in ref:
        return False
    f, h = ref.split("#", 1)
    for c in chunks:
        if os.path.basename(c.get("path") or "") == f and (c.get("heading") or "") == h:
            return True
    return False

def rag_tokens(s):
    for b in s.get("budget", []):
        if b.get("component") == "rag":
            return b.get("used", 0)
    return 0

base_tokens, gate_tokens, base_recall, gate_recall, keep_rates, dec_tokens = [], [], [], [], [], []
for i, q in enumerate(queries, start=1):
    refs = q.get("relevant", [])
    off_s, off_snap = snap(f"{tmp}/q{i}.off.json"), snap(f"{tmp}/q{i}.off.json")
    on_sse, on_snap = f"{tmp}/q{i}.on.sse", snap(f"{tmp}/q{i}.on.json")

    off_kept = [c for c in off_snap.get("chunks", []) if not c.get("humanOverride")]
    base_tokens.append(rag_tokens(off_snap))
    base_recall.append(sum(1 for r in refs if matches(r, off_kept)) / len(refs) if refs else 0.0)

    retrieved = rag_chunks(on_sse)
    on_kept = [c for c in on_snap.get("chunks", []) if not c.get("humanOverride")]
    gate_tokens.append(rag_tokens(on_snap))
    gate_recall.append(sum(1 for r in refs if matches(r, on_kept)) / len(refs) if refs else 0.0)
    keep_rates.append(len(on_kept) / len(retrieved) if retrieved else 0.0)
    d = on_snap.get("decision") or {}
    p = d.get("planner") or {}
    g = d.get("gate") or {}
    dec_tokens.append((p.get("promptTokens", 0) + p.get("completionTokens", 0)
                       + g.get("promptTokens", 0) + g.get("completionTokens", 0)))

def mean(xs):
    return statistics.mean(xs) if xs else 0.0

base_r, gate_r = mean(base_recall), mean(gate_recall)
base_t, gate_t = mean(base_tokens), mean(gate_tokens)
print("=== decision-layer evaluation: %s ===" % label)
print("queries: %d" % len(queries))
print("%-22s %12s %12s" % ("metric", "baseline(off)", "gated(on)"))
print("%-22s %12.3f %12.3f" % ("recall (judged)", base_r, gate_r))
print("%-22s %12.1f %12.1f" % ("injected rag tokens", base_t, gate_t))
print("%-22s %12s %12.3f" % ("keep rate", "-", mean(keep_rates)))
print("%-22s %12s %12.1f" % ("decision tokens/turn", "-", mean(dec_tokens)))
tok_delta = (base_t - gate_t) / base_t * 100 if base_t else 0.0
rec_delta = (gate_r - base_r) * 100
print()
print("token reduction: %.1f%%   recall delta: %+.1f pts" % (tok_delta, rec_delta))
verdict = "HOLDS" if (gate_t <= base_t and gate_r >= base_r - 0.02) else "NOT MET"
print("claim 'gating reduces injected tokens without recall loss': %s" % verdict)

if record:
    doc = os.path.join(repo, "docs/writing-assistant/research/decision-eval.md")
    ts = datetime.date.today().isoformat()
    row = ("| %s | %s | %.3f | %.1f | %.3f | %.1f | %+.1f | %.1f%% | %s |\n"
           % (ts, label, gate_r, gate_t, mean(keep_rates), mean(dec_tokens), rec_delta, tok_delta, verdict))
    header = ("\n## Live runs\n\n"
              "| date | config | recall | injected tokens | keep rate | decision tokens | recall Δ | token Δ | verdict |\n"
              "|---|---|---|---|---|---|---|---|---|\n")
    with open(doc, "a") as f:
        if "## Live runs" not in open(doc).read():
            f.write(header)
        f.write(row)
    print("\nrecorded to %s" % doc)
PY
