# Handoff — Phase E3: Orchestration, daemon reliability, parity (ADR-0046, ADR-0040)

Prompt for a fresh session. Repos: `~/Documents/Liv/Projects/texteditor`
(real path `/Volumes/Ex-SSD/Documents/Liv/Projects/texteditor`) and the sibling
`~/Projects/macos-dev-config` (real path `/Volumes/Ex-SSD/Documents/Liv/Projects/macos-dev-config`).
Do not commit unless asked.

Status: E1 and E2 landed (the Ratatui client has chat, reader, tabs, meter,
RAG/context, tray, corpus tree, locate, C5 labels, approve/write-through). Phase
F not started. OpenTUI stays **frozen in-tree** — do not delete `client/tui/`.

## Read first (in order)
1. `docs/writing-assistant/plans/implementation-sequence-context-engine.md` —
   Phase E3 only.
2. `docs/writing-assistant/adr/0046-ratatui-tui-v2.md` §8 (fleet rendering
   only), §9 (parity checklist); `adr/0040-fleet-observability-surface.md`
   (recorded note: move fleet orchestration engine-side); `adr/0043` (build
   script conventions); `adr/0021` (sidecar/port policy).
3. macos-dev-config: `docs/contracts/daemon-http.md`,
   `SERVING-MANIFEST.md`, `internal/fleetdaemon/{runner.go,daemon.go,state.go}`,
   `tools/serve.sh`, `internal/fleetdaemon/daemon_test.go`.
4. Engine: `server/internal/fleet/`, `server/internal/loop/loop.go` (validate /
   resolve), `server/cmd/texteditor/main.go`.

## Scope
Phase E3 only: engine-side model orchestration, daemon reliability, build gate,
parity verification, retirement state. No Phase F work.

## Deliverables

### 1. Engine-side model orchestration (ADR-0040 recorded note)
- On a degraded resolve (fallback selected or default down), the engine makes
  **one flagged, bounded attempt** to `Fleet.Start(defaultModel)` and then
  re-resolves; the attempt and its outcome are labeled (done/degraded fields or
  a typed event), never silent and never unbounded.
- The TUI renders `GET /fleet` and issues raw lifecycle verbs only; no
  busy/poll/switch logic in the client (E2 must not have copied any).
- Config flag for the behavior (pipeline or an engine flag); tests with a stub
  fleet (start succeeds → no degradation; start fails → labeled degrade).

### 2. macos-dev-config daemon reliability (verified defects)
- `internal/fleetdaemon/runner.go:44-50`: `NAME` is **not** passed to
  `serve.sh` (logs land in `var/serve-.log`; `GET /log/{name}` reads the wrong
  file). Pass the manifest model name; `modelLabel()` (runner.go:55-57)
  currently returns the runner.
- `daemon.go` `Stop` (~lines 322-340): kills the recorded `exec.Cmd` (the
  `serve.sh` wrapper, already exited); detached runners survive and the daemon
  reports `down`. Stop through `serve.sh stop` (its pkill pattern, serve.sh
  ~135-151) or reproduce the pattern safely; make stop idempotent and truthful.
- `state.go`: live state is in-memory; after a daemon restart `status` reports
  `down` for a live runner and `start` fails `port-in-use`. Reconcile by health
  probe when state is `unknown` (probe `/health`, `/v1/models`, `/api/tags` as
  `daemon.go` already does).
- `daemon.go:293,313`: the `SERVE_PORT_<NAME>` remap hint is advertised but
  unimplemented — implement it or remove the hint from code + contract
  (`docs/contracts/daemon-http.md`), keeping the mirror in texteditor in sync.
- Delegate log-path mapping (`serve-needle.sh`, `serve-qwen.sh` write
  `var/serve-<name>.log` names that do not match `GET /log/{name}`).
- Tests in `daemon_test.go`; run `go test ./...`, `go vet`, `gofmt` before
  touching the live launchd agent. Do not reload launchd until tests pass.

### 3. Build + parity
- `tools/build-tui-rs.sh` gains the test gate (`cargo test`, fmt) and remains
  CI-shaped: repo-root resolution from `BASH_SOURCE`, no machine paths
  hardcoded, SSD env documented not auto-set (ADR-0043 convention).
- Parity checklist verified end-to-end against the fixed engine: chat stream,
  reader, tabs, meter, RAG/context, diff/approve, write-through status, fleet
  render, paste (ADR-0046 §9 as amended by ADR-0050).
- Retirement state: OpenTUI stays frozen in-tree; ensure builds/docs no longer
  list it as active (README, `docs/contribute.md`, plan/status wording).

### 4. Docs
- `status.md`: Phase E (E1–E3) complete, parity evidence, daemon fixes, the
  OpenTUI frozen-in-tree decision.
- `docs/contribute.md` + README: `client/tui-rs` build/run instructions; the
  external-SSD Rust env; OpenTUI marked frozen.
- macos-dev-config docs (`daemon-http.md`, `SERVING-MANIFEST.md`,
  `inference-readme.md`) for the reliability changes; keep the texteditor
  contract mirror in sync.

## Checkpoints
- **E3a — engine orchestration**: auto-start policy + tests; TUI fleet panel
  verified render-only.
- **E3b — daemon reliability**: all defects fixed + tested; live verification
  (start/stop/status/log through the daemon) with the launchd agent reloaded
  only after green tests.
- **E3c — build gate + parity + retirement/docs**: build script gate, parity
  checklist walked, docs updated.

If the session runs short, stop after a fully green checkpoint and report.

## Verify
```
cd server && CGO_ENABLED=0 go test -count=1 ./... && CGO_ENABLED=0 go vet ./... && gofmt -l server/
cd ~/Projects/macos-dev-config && go test ./... && go vet ./... && gofmt -l .
cd client/tui-rs && cargo test && cargo fmt --check
./tools/build-tui-rs.sh
# live: daemon start/stop/status/log per model; parity checklist
```

## Report
Files changed in both repos, tests added, exact commands run, deviations with
rationale, the completed parity checklist, and any residual gap before Phase F.
