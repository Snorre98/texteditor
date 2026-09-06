# Handoff — Plan D1 (router enablement): the serving side now, the ML job later

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

**Track 1 (A/B/C + the D2–D5 router seam) and Track 2 (E/F — deployment + the
Tauri editor) are fully landed.** The three targets (TUI, desktop WebView, web)
run one engine over one contract (ADR-0014). The last roadmap item is **D1 —
router enablement** (ADR-0028 §7): the seam exists end-to-end (D2–D5); D1 turns it
*on*.

**D1 is gated.** `research/parked-needle-router.md` pins the enablement triggers;
ADR-0028 §7 keeps `toolCalling: "native"` the byte-identical baseline until one
fires. Step zero is to record which trigger fired (or the explicit operator
decision to enable). This plan splits D1 so that **everything except the actual ML
fine-tune lands now**, and the fine-tune is a discrete later step that drops in.

### Landed (D2–D5 seam + Track 2 — do not redo)

- **`ToolDecider` module** — `server/internal/tooldecider/decider.go`: `SignalTool()`
  + `Decide(ctx, intent, RouterContext) → RouterResult`; sealed interface at
  `interface.md §8b`; wired only when a mode sets `toolCalling: "router"`.
- **Loop routing** — `server/internal/loop/loop.go`: in router mode, splices
  `SignalTool()` instead of `AllowlistFor(mode)`, intercepts `request_tool`, calls
  `Decide`, dispatches `Invoke(name, args)` on a confident decision; second
  `Meter.Attribute` row tagged `model=needle-router` (no `meter_events` change).
- **Fail-fast startup gates** — `server/internal/routergate/gate.go`: `Check`,
  `ToolSetHash`, `RouterModelName = "needle-router"`, `ErrRouterUnavailable`
  (`mode-refs-router-unavailable`) + `ErrToolsStale` (`router-tools-stale`), run at
  the composition root in `cmd/texteditor/main.go`.
- **Gate plumbing** — `FleetGateway.Fingerprint(name)` (`interface.md §1`);
  `Completion.FinishReason` (`interface.md §2`). The daemon already projects
  `fingerprint` for `source.kind == "needle"` and carries the `needle` source kind
  + schema (`macos-dev-config/internal/fleetdaemon/`, `fleet-manifest.schema.json`).
- **Mode schema + data** — `toolCalling` enum in `server/config/schemas/mode.schema.json`;
  all four shipped modes are `"native"`.
- **Behaviors + tests** — `tool-routing.feature` + `tooldecider`/`routergate`/`loop`
  boundary tests, green.

### Settled (ADR-0028 "Recorded amendments" — do not re-ask)

1. **Gates at the composition root**, not the Mode registry.
2. **Refusal realization** (`Confidence < τ` / empty-call): a "no tool needed"
   tool-result for `request_tool`, then **one** bounded writer round whose `stop`
   stream is the answering phase — no error event, no extra router call.
3. **Confidence channel:** the facade's confident response is a completion with
   `finish_reason: "tool"` and content `{"name","args","confidence"}`; refusal/empty
   is an empty completion. τ is applied inside the decider.
4. **`needle-router` is resolved by name** (the `nomic-embed` pattern), never a
   mode's `defaultModel`.

### Locked decisions for this plan

1. **The serving side is owned in `macos-dev-config`**, but **tied back** to this
   repo: the facade's protocol is a canonical contract there, mirrored here with a
   drift check (the existing `daemon-http.md` pattern, ADR-0033 §3) — so a second
   needle `.cact`/variant later conforms to one contract or the sync test fails.
2. **Smoke-test against a committed stub needle** binary (no real `.cact` needed to
   verify the facade's protocol mapping).
3. **`cmd/toolhash` lives in texteditor** — the single drift-free source of the
   tool-set hash + vocabulary the fine-tune consumes.
4. **Manifest `needle`/`needle-router` entries land now** with an empty fingerprint
   (safe: no mode flips, so `routergate.Check` is a no-op).

## Build order (mandatory)

### Phase 1 — texteditor: the ML-ready seam

1. **`server/cmd/toolhash/main.go`** — `go run ./cmd/toolhash` loads the tool
   registry (`tool.Load()`, pure config, no daemon) and prints JSON
   `{ "hash": "<routergate.ToolSetHash(registry.List())>", "tools": [ {name,
   description, parameters}… sorted by name ] }`. It reuses the exact hash
   `main.go` already computes for the gate, so the fine-tune never re-implements it
   (reimplementing it *is* the `router-tools-stale` drift class). Add a test that
   the output parses and `hash` equals `ToolSetHash`.
2. **Mirror the facade contract** — `docs/writing-assistant/contracts/needle-facade.md`
   (MIRROR banner, like `daemon-http.md`) plus a
   `server/internal/routergate/contract_mirror_test.go` `TestNeedleFacadeContractMirror`
   modeled on the fleet mirror test (skips when the sibling `macos-dev-config` is
   not checked out).

### Phase 2 — macos-dev-config: the serving side

3. **`tools/serve-needle.sh`** — the verb-convention wrapper (`serve/start/stop/
   status/log`, mirroring `serve-qwen.sh`) around the facade binary.
4. **`cmd/serve-needle/main.go`** (Go; this repo already has a `go.mod` + the Go
   fleetdaemon) — a non-streaming `POST /v1/chat/completions` facade that: extracts
   the intent from the decider's prompt, shells the needle binary (configurable
   `NEEDLE_BIN`/`NEEDLE_CACT`), and maps its output to ADR-0028 §7/amendment-3 —
   confident → `finish_reason:"tool"` + `{"name","args","confidence"}`; refusal/empty
   → empty completion. A `/health` endpoint backs the daemon's `status` verb.
5. **Stub needle** (test fixture, e.g. `testdata/needle-stub.sh` or a tiny Go stub)
   emitting canned confident/refusal output, so the mapping is curl-verifiable
   today.
6. **`tools/needle-finetune.sh`** — the ML-ready scaffold: consumes `cmd/toolhash`
   output, has a clearly-marked `TODO` slot for the actual training invocation,
   archives the produced `.cact` (mirroring `hf-archive.sh`), and writes
   `source.fingerprint` into `models.json`.
7. **`models.json`** — add `daemons.needle` (`runner:"delegate"`,
   `delegate:"serve-needle.sh"`) and `models.needle-router` (`source.kind:"needle"`,
   `source.file:"needle2.cact"`, `source.fingerprint:""`).
8. **Canonical contract** `docs/contracts/needle-facade.md` — the facade protocol
   and the needle-stdout assumption (below), the mirror's source of truth.

### Phase 3 — docs: record the split

9. Mark Phases 1–2 landed in this file and shrink the remaining section to the
   single deferred ML job; record the four locked decisions.

## The "ready for ML" seam

**Built now:** facade, manifest entries, `cmd/toolhash`, the finetune scaffold,
stub-verified protocol mapping.

**Deferred (the ML job, done later):** run the Needle 2 fine-tune on the tool
vocabulary → produce `needle2.cact` → `needle-finetune.sh` archives it and records
the fingerprint → flip one mode to `toolCalling: "router"` → the
`router-tools-stale` gate clears.

**Single ML-touch-point:** the facade's needle-*stdout* parser is written against
the stub + the `needle-facade.md` assumption. If the real `.cact` output format
differs, only that parser and the contract doc change — nothing in the engine.
Until the real format is known, the contract pins: a confident decode is
`<tool-name>\n<json-args>\n<confidence>` on stdout; a refusal is empty stdout
(with a non-zero exit allowed for "empty call"). This is the one assumption to
finalize at ML time.

## Verification gates

- texteditor: `CGO_ENABLED=0 go test -count=1 ./...` stays green; the mirror test
  passes; `go run ./cmd/toolhash` output matches `routergate.ToolSetHash`. **No
  spec change** — no `api/openapi.yaml` edit and no codegen regen (the router is
  client-transparent, ADR-0028).
- macos-dev-config: manifest validates against the fleet schema; daemon `list`
  reports `needle-router` with `runner: delegate` and an empty `fingerprint`; a curl
  smoke of the facade against the stub proves both the confident and refusal
  mappings.
- Gates re-confirmed: `router-tools-stale` fires on a flipped mode + empty
  fingerprint, `mode-refs-router-unavailable` on a router mode with no resolvable
  needle-router (existing `routergate` boundary tests).

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` — Plan D (D1 row + the
   sequencing note under it).
2. `docs/writing-assistant/adr/0028-tool-decider.md` — normative, **including the
   "Recorded amendments" tail**.
3. `docs/writing-assistant/research/parked-needle-router.md` — the four triggers +
   why enablement is deferred.
4. `docs/writing-assistant/adr/0018-fleet-manifest-serving.md` §3 (`delegate` runner)
   and `adr/0030-fleet-substrate-pure-llamacpp-mlx-metal.md` (runner enum).
5. `docs/writing-assistant/adr/0032-control-daemon-source-location.md` +
   `adr/0033-control-daemon-source-move.md` — the serving side lives in
   macos-dev-config; texteditor keeps mirrored contracts + a sync check (§3).
6. `docs/writing-assistant/contracts/interface.md` §1 (`Fingerprint`), §2
   (`Completion.FinishReason`), §8b (`ToolDecider`); `contracts/daemon-http.md`
   (the `fingerprint` projection). The mirror test is in
   `server/internal/fleet/contract_mirror_test.go` — copy its pattern.
7. `docs/writing-assistant/behaviors/tool-routing.feature`;
   `server/internal/routergate/gate.go` (the `ToolSetHash` algorithm);
   `macos-dev-config/tools/serve-qwen.sh` + `tools/hf-archive.sh` (the facade +
   archive conventions to mirror).

## Hard constraints (never violate)

- **ADR-0003:** Needle never enters the engine binary — no CGO, single static Go
  binary preserved. The C++ engine + `.cact` live only behind the facade in
  macos-dev-config.
- **ADR-0025/0027:** the engine reaches serving only via Fleet → daemon; it never
  reads the manifest or shells `serve.sh`/`serve-needle.sh`. The one sanctioned
  exception is the `fingerprint` field (ADR-0028 §4).
- **ADR-0030:** the `runner` enum is `llama.cpp | mlx-lm | mlx-vlm | delegate`;
  needle rides `delegate`. No new runner kind.
- **Fail-fast, never silent-degrade:** a stale/missing router refuses startup.
- **`native` stays the byte-identical baseline** — the router is a per-mode toggle.
- **The fingerprint must be the exact `routergate.ToolSetHash`** — and the fine-tune
  must consume it from `cmd/toolhash`, not recompute it.
- **The facade contract stays mirrored + drift-checked** (ADR-0033 §3) — a new
  needle file conforms to the same `needle-facade.md` or the sync test fails.

## Then continue

D1 landed → the roadmap is complete. Remaining deferred: the `InferenceControl`
surface (`architecture.md` risk #9) — a future *sibling* interface behind the
Provider seam, not a planned phase.

## Report back

At each milestone: what landed, which tests pass, and any place the docs forced a
stop or a judgment call. Specifically flag: (a) **which enablement trigger fired**
(or the explicit operator decision), recorded in `research/parked-needle-router.md`;
(b) the **facade protocol realization** against ADR-0028 §7/amendment-3 (Go facade,
stub-verified, `finish_reason:"tool"` + JSON content) and its **mirror contract**;
(c) the **fingerprint wiring** — `cmd/toolhash` emits exactly `routergate.ToolSetHash`
and `needle-finetune.sh` records it into the manifest.
