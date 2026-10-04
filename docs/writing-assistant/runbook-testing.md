# Runbook — full testing

Scope: everything from static gates to live end-to-end, including the Phase F
Laya decision layer and the F4 evaluation. Commands assume the repo root
(`/Volumes/Ex-SSD/Documents/Liv/Projects/texteditor`) unless noted.

Companion docs: [`contribute.md`](../../contribute.md) (build/codegen),
[`status.md`](status.md) (what has landed), [`research/decision-eval.md`](research/decision-eval.md)
(the F4 method).

## 0. Environment

```sh
# Rust lives on the external SSD
export RUSTUP_HOME=/Volumes/Ex-SSD/caches/rust CARGO_HOME=/Volumes/Ex-SSD/caches/cargo
export PATH="$CARGO_HOME/bin:$PATH"

# Tools
go version && bun --version && cargo --version && openapi-to-rust --version
```

Ports and paths:

| Thing | Value |
|---|---|
| Engine | `http://127.0.0.1:9100` (smoke default; `ENGINE_URL` overrides) |
| Control daemon | `http://127.0.0.1:9300` (`DAEMON_URL`) |
| Model daemons | `808x` / `809x` (see `macos-dev-config/models.json`) |
| Laya | `127.0.0.1:8095` |
| `ALLOWED_ROOTS` | `$HOME` by default; temp vaults must live under it |
| Engine data | `~/.local/share/texteditor` (use a throwaway `--data` for clean runs) |

## 1. Static gates (no services)

```sh
# Engine
cd server && CGO_ENABLED=0 go test -count=1 ./... && CGO_ENABLED=0 go vet ./... && gofmt -l .

# Rust TUI (ADR-0046)
cd client/tui-rs && cargo test && ./../../tools/build-tui-rs.sh --test

# TS TUI (frozen; still gated)
cd client/tui && bun test && bun run typecheck

# Control daemon
cd /Users/snorresaether/Projects/macos-dev-config && go test ./...
```

Notes:

- `cargo fmt --check` is **environment-red**: rustfmt 1.9 reformats the generated
  block-doc closing delimiters non-idempotently, and HEAD fails identically. The
  tui-rs gate is `cargo test` + `build-tui-rs.sh --test`. A fmt diff is not a
  regression; the codegen lockstep runs `cargo fmt` after regeneration.
- Tauri (`client/tauri`, `src-tauri`) is frozen (ADR-0044) — skip its suites.

## 2. Codegen lockstep (drift check)

```sh
cd server && go generate ./... && cd ..
cd client/tui-rs && openapi-to-rust generate -c openapi-to-rust.toml && cd ../..
git status --short api/openapi.yaml server/internal/genapi client/tui-rs/src/generated
# Expect NO diff beyond your intentional change.
```

`client/tui` and `client/tauri` are frozen: do **not** regenerate them. Only
`client/tui-rs` is regenerated in lockstep (ADR-0044/0046).

## 3. Engine bring-up (local)

```sh
cd server
go run ./cmd/texteditor --port 9100 --data /tmp/te-run --daemon http://127.0.0.1:9300
# add ALLOWED_ROOTS=<an allowed parent> to bound browsing/indexing

curl -s localhost:9100/health | python3 -m json.tool   # status ok + baseUrl
```

Use a fresh `--data` dir per run for a clean shard state.

## 4. Control daemon + fleet

```sh
cd /Users/snorresaether/Projects/macos-dev-config
tools/build-fleetdaemon.sh
# Foreground for a test session:
bin/fleetdaemon --manifest models.json --addr 127.0.0.1:9300 --var var --serve-sh tools/serve.sh
# or launchd (always-on):
launchctl load ~/Library/LaunchAgents/com.macosdev.fleetdaemon.plist

curl -s localhost:9300/list | python3 -m json.tool
curl -s localhost:9300/status/all | python3 -m json.tool

# Bring up what the smokes need:
curl -sX POST localhost:9300/start/nomic-embed    # embedding (corpus/RAG/decision smokes)
curl -sX POST localhost:9300/start/gemma4-26b-moe  # or any model tagged editor (locate/write-through)
```

### 4a. Laya (Phase F)

```sh
# One-time install (Python 3.10+; pulls torch)
uv tool install 'laya[serve]'   # or: pip install 'laya[serve]'

# Start via the runner wrapper (sets LAYA_HOST/PORT, preloads checkpoints)
/Users/snorresaether/Projects/macos-dev-config/tools/serve-laya.sh start
curl -s localhost:8095/health | python3 -m json.tool

# Wire check: one typed decision
curl -s localhost:8095/v1/systemone -H 'content-type: application/json' -d '{
  "state":"The writing request needs corpus passages.",
  "questions":{"retrieve":{"type":"noul","instructions":"Does this need corpus retrieval?"}}}' | python3 -m json.tool

# The engine must see it:
curl -s localhost:9100/models | python3 -m json.tool | grep -A3 '"laya"'
curl -s localhost:9100/decision | python3 -m json.tool
```

## 5. Automated live smokes

Each preflights `/health` (and the model it needs) and is excluded from
`go test`. Set `SMOKE_BASE` to a directory under `ALLOWED_ROOTS`.

```sh
tools/smoke-rag.sh            # workspace -> multi-root corpus -> index -> turn (rag+context) -> snapshot -> evict -> 403
tools/smoke-corpus.sh         # corpus tree routes (Ctrl+K): scope/index/status/stale/evict/403
tools/smoke-locate.sh         # /locate anchoring end to end (needs an editor-tagged model)
tools/smoke-write-through.sh  # approve boundary: 409 no-clobber -> overwrite -> file changed
tools/smoke-lifecycle.sh      # E4: POST /open, open-or-resume, atomic accept, GET /events progress
tools/smoke-decision.sh       # Phase F: planner/gate, snapshot record, meter row, pins bypass, degraded

ENGINE_URL=http://127.0.0.1:9100 SMOKE_BASE=/an/allowed/root tools/smoke-rag.sh
SMOKE_STAGE_DIRECT=1 tools/smoke-write-through.sh   # if the model cannot emit structured tool_calls
```

## 6. Decision layer live test (Phase F)

`tools/smoke-decision.sh` runs the full path automatically:

```sh
ENGINE_URL=http://127.0.0.1:9100 SMOKE_BASE=/an/allowed/root tools/smoke-decision.sh
```

It asserts, in order:

1. `GET /decision` returns the global policy block.
2. A temp workspace → corpus → index.
3. A turn with a per-turn `ContextPolicy.decision=planner+gate` emits `rag` + `context`.
4. `GET /turns/{id}/context` carries `decision.planner` (retrieve/thinking/breadth/checkpoint),
   `decision.gate.chunks[]` with scores, survivors (`chunks`) equal to the kept
   candidates, a labeled `ContextDrop{component:rag,reason:gate}` per drop, and a
   `rag` event that is **pre-gate** (≥ survivors).
5. `GET /sessions/{id}/meter` has a `decision` component row.
6. A session `PUT /sessions/{id}/context {"decision":"planner+gate"}` gates the next turn
   without a per-turn override.
7. A pinned chunk survives the gate and is labeled `humanOverride`.
8. `SMOKE_LAYADOWN=1` stops `laya`, reruns, and asserts `decision.degraded=true`
   (fail-open, all chunks kept), then restarts it:

```sh
SMOKE_LAYADOWN=1 tools/smoke-decision.sh
```

Also test by hand: session `autoRag:false` (the planner cannot re-enable
retrieval), and an explicit `ContextPolicy.thinking` overriding Laya.

## 7. TUI-rs manual run + live tests

```sh
./tools/build-tui-rs.sh
client/tui-rs/target/debug/texteditor-tui-rs ~/vault
```

Checklist:

1. workspace opens; Ctrl+W / Ctrl+S browse and list sessions
2. Ctrl+K corpus tree: `s` scope, `i` index/rebuild (watch job progress), `e` evict; on-disk markdown untouched
3. Ctrl+T tray: pin, exclude, edit the retrieval query, toggle auto-RAG, **`d` cycles the decision layer** (off → planner → planner+gate); confirm the inspector shows `pinned`/`humanOverride`/drops
4. Ctrl+I inspector: meter, context, thinking/budget, and the **decision section** (planner, gate kept/dropped + scores + checkpoint, `degraded` label)
5. Ctrl+R reader renders the engine's markdown (read-only)
6. `@` attaches a mention; run a turn; Ctrl+X cancels with a labeled `done {cancelled:true}`
7. paste a paragraph with a leading `/locate`; resolve the picker; approve the anchored edit
8. Ctrl+F fleet: start/stop/provision; the engine auto-starts a down preferred model on a turn
9. write-through only on approve (Ctrl+A); an external change is a labeled 409, no clobber

Live HTTP tests (engine running, no model needed):

```sh
cd client/tui-rs && cargo test --test live_engine -- --ignored --nocapture --test-threads=1
```

## 8. F4 evaluation sweep

`tools/eval-decision.sh` runs the golden fixture at the **current** engine config
and prints recall / injected-token / keep-rate metrics plus the claim verdict:

```sh
SMOKE_BASE=/an/allowed/root tools/eval-decision.sh
# a real corpus:
SMOKE_CORPUS=~/thesis SMOKE_QUERIES=~/thesis/queries.jsonl tools/eval-decision.sh
```

The `gateThreshold` / `breadthTopK` sweep is config-driven (the policy is
embedded), so sweep by editing `config/pipeline.json`, rebuilding/restarting the
engine, and rerunning with a label; `--record` appends the row to
`research/decision-eval.md`:

```sh
# edit server/config/pipeline.json decision.gateThreshold (0.3..0.7) and breadthTopK.few/many
# restart the engine, then:
SMOKE_LABEL="tau=0.4,breadth=few" SMOKE_BASE=/an/allowed/root tools/eval-decision.sh --record
```

Also compare checkpoints (`english` / `multilingual` / `typed-decisions`) and,
where available, fp32 vs ONNX int8 exports. Set the shipped defaults from the
sweep and record them in `status.md`.

## 9. Negative / failure cases

| Case | Expectation |
|---|---|
| Control daemon down | `/fleet` 200 `control:unreachable`; `/models` hard-fails; already-up turns unaffected |
| Preferred model down | engine makes one bounded auto-start, then a labeled fallback (`/fleet`) |
| Laya down | turn proceeds fail-open; snapshot `decision-degraded`; no silent drop |
| Path outside `ALLOWED_ROOTS` | typed `path-outside-allowed-roots` 403, never silent |
| External file change on approve | typed `file-changed-externally` 409, no bytes written |
| Fixed+pinned+user over the model window | typed `context-window-exceeded`, before any provider call |
| Session hard budget | `session-budget-exceeded` unless compaction rescues |
| Cancelled turn | labeled `done{cancelled:true}`, partial usage metered |

## 10. Troubleshooting

- `cargo fmt --check` red → expected (§1); use `cargo test` + the build script.
- Smoke refuses to run → `/health` unreachable, or the required model is absent
  from `/models`; or `SMOKE_BASE` is outside `ALLOWED_ROOTS`.
- `laya-serve` not found → `uv tool install 'laya[serve]'`; override `LAYASERVE_BIN`.
- Planner chose `retrieve=false` on a smoke query → make the query clearly
  corpus-relevant, or inspect `decision.planner` in the snapshot.
- `mlx_lm.server` missing → reinstall `mlx-lm` (see the serving-stack commit notes).

## Known gaps

- **E3.3 parity checklist / E3.4 retirement** are not yet recorded in `status.md`.
- The F4 live sweep must run against a real runner + thesis-scale corpus before
  the ADR-0053 claim is considered measured (defaults are provisional).
