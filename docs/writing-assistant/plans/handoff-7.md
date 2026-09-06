# Handoff — resume implementation (next session)

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

**Milestone 6 landed: the router seam (D2–D5) is built and off by default.**
`toolCalling: "native"` remains the byte-identical baseline (all shipped modes
are `native`; the existing native/agentic tests pass unmodified). The router
can be *turned on* by flipping a mode, but nothing opts in and no Needle serving
exists — enablement (D1) stays deferred, gated by the triggers below. Track 1 is
otherwise complete; the POC acceptance was already reached at the wire level in
Milestone 5 (a *live token stream* still needs real weights on the SSD — see
below; that is provisioning, not code).

### Landed + tested this session (committed in `texteditor`, `aa1c66b`)

- **`internal/tooldecider` (D2)** — Retriever-style service over two sealed
  subsets (`Resolver` = Fleet.Resolve, `Chatter` = Provider.Chat).
  `SignalTool()` returns the exact ADR-0028 §2 `request_tool` definition (not
  registered anywhere); `Decide` resolves `needle-router` by name
  (`ResolveOpts{ModeTag: "needle-router"}`, the nomic-embed pattern), renders a
  private prompt, calls `Chat` once, and returns `RouterResult{Decision, Usage}`
  with the ADR-0028 §5 `Breakdown` mapping (SystemPrompt=instruction,
  Tools=candidate schemas, Rag/History=context, User=intent, Thinking=0).
- **`internal/routergate` (startup gates, D-adjacent)** — `ToolSetHash([]ToolDef)`
  (sha256 over name-sorted, canonical-compacted `(name, description, parameters)`;
  algorithm documented so D1's `needle finetune` replicates it) and
  `Check(modes, modelPresent, fingerprint, toolHash)` → typed
  `mode-refs-router-unavailable` / `router-tools-stale`. Runs at the
  **composition root** (`cmd/texteditor/main.go`), not in the Mode registry
  (sequencing note). A no-op when no mode opts in.
- **Loop toggle + routing (D3)** — `loop.Deps.Decider` (sealed subset defined in
  the loop package; nil = native). Router mode splices `[SignalTool()]` instead
  of the allowlist (the real allowlist rides along as the `Decide` candidate
  set), intercepts `request_tool`, and implements `deciding`
  (state-machine §1.2): confident → dispatch the resolved tool through the
  *existing* native executor/observe machinery; refusal / `router-unreachable`
  error → a "no tool" tool-result message then **one bounded writer round** whose
  `stop` stream is the answering phase (recorded amendment — see ADR-0028 note
  below). Defensive guard: router mode with no wired decider emits an error
  event.
- **Second meter call (D4)** — `routeTool` calls `Meter.Attribute` again with
  `model = "needle-router"`, turn/session-scoped, before the outcome is known
  (refusals are metered too). No `meter_events` schema change. Note: the wire
  `MeterEvent` has no `model` field, so the TUI sees the router row as a second
  meter event folded into the session tally; the DB row is tagged correctly.
- **Public API lock (D5)** — `interface.md` §8b and the module-boundaries row
  existed since A1–A4; this session added the recorded amendments below and Q5
  boundary tests (tooldecider, routergate, loop router paths, fleet
  `Fingerprint`).

### Recorded contract amendments (not silent)

- **`daemon-http.md` §2** (canonical in `macos-dev-config`, mirrored here): `list`
  entries gain an optional `fingerprint` field, projected **only** for
  `source.kind == "needle"` — the ADR-0033 §4 backward-compatible pattern.
  Canonical + mirror edited in the same change (the `internal/fleet` mirror test
  enforces sync).
- **`interface.md` §1**: `FleetGateway` gains `Fingerprint(name) (string, error)`
  — the *single* source-derived field the engine ever reads (ADR-0016 §1
  carve-out, justified by ADR-0028 §4's gate).
- **`interface.md` §2**: `Completion` gains `FinishReason` (additive;
  `parseCompletion` already parsed it and dropped it).
- **ADR-0028 "Recorded amendments" block** pins three implementation choices:
  (1) fingerprint gate mechanism via the `list` projection; (2) refusal
  realization = one bounded writer round ("no extra call" = no extra *router*
  call); (3) confidence channel = content JSON `{name,args,confidence}` with
  `finish_reason: "tool"` (deviation from §7's "stop-reason/header" — the
  Provider carries no header channel); (4) τ is applied **inside** the decider —
  a refusal is the zero `Decision` (`Name == ""`), the loop dispatches iff
  `Name != ""`.
- **`state-machine.md` §1.3** — the `deciding → answering` realization note.
- `module-boundaries.md` / `architecture.md` Fleet rows gain `Fingerprint`;
  `implementation-sequence.md` D2–D5 + sequencing note + milestone 6 marked
  landed.

### Cross-repo (macos-dev-config — committed, `36ccc05`)

`docs/contracts/daemon-http.md` and `internal/fleetdaemon/manifest.go`
(`daemonEntry.Fingerprint` + `listEntries` projection) are committed there. The
drop-shipped daemon **binary was not rebuilt/reloaded** — the field is inert
until a `needle` manifest entry exists (D1), so this is safe to defer, but D1's
standup must rebuild (`tools/build-fleetdaemon.sh`) and reload the always-on
agent first.

### TUI (test-only)

`tests/ui.test.tsx` had a deterministic flake (markdown parser worker racing the
test renderer's idle detection — it failed on the clean base commit too). Fixed
with a `retryFrames` wrapper; **no `src/` change**. `bun run gen` was NOT run and
the OpenAPI spec is untouched this session.

### Verification

- `CGO_ENABLED=0 go test ./...` green (uncached), including the new router tests:
  `TestRouterConfidentDispatch` (dispatch + candidate + `[needle-router,
  <writer>]` meter rows), `TestRouterRefusalAnswers` (no error event, done
  lands), `TestRouterDecideErrorDegrades` (`router-unreachable` + graceful
  answer), `TestRouterSplicesRequestTool`, `TestNativeNeverConsultsDecider`,
  `TestRouterUnwiredEmitsError`; plus tooldecider/routergate/fleet-`Fingerprint`
  suites.
- `bun test` 28 pass + `bunx tsc --noEmit` green in `client/tui`.
- Boot smoke: engine boots against a live daemon fixture, `/health` ok, gates
  skip with all-native modes.

## Still open (explicit list; do not silently choose)

### Plan D — enablement (D1, deferred + gated)

The seam exists; enabling requires, in order (ADR-0028 §7, §1 of the parked
research card): rebuild + reload the daemon binary (the macos-dev-config side is
committed) → `serve-needle.sh` + OpenAI facade (encodes confident = content
JSON + `finish_reason: "tool"`, refusal = empty completion) → manifest gains
`daemon "needle"` (`delegate`) and model `needle-router`
(`source.kind: "needle"`, `source.fingerprint` = `ToolSetHash` value recorded at
fine-tune) → fine-tune Needle on the engine's tool vocabulary → flip a mode to
`toolCalling: "router"`. Only when an enablement trigger fires (tool set > ~15,
measured tool-calling accuracy/refusal problem, or a measurably weak writer on
an agentic mode) — `research/parked-needle-router.md`.

### Verification gates carried forward

- `tool-routing.feature` scenarios are now asserted as Go unit tests (see the
  router tests above) — wire as CI assertions whenever CI exists. The two
  startup-gate scenarios are covered by `routergate` tests.
- `versioning.feature`, `provider-hotswap.feature` — still gated on Track 2.
- `serving-control.feature` "TUI switches models" is store unit tests
  (`tui/tests/store.test.ts`); a UI-level assertion can still be added later.

### Still blocking a *live* token-stream demo (external, not code)

`macos-dev-config` has no weights on disk (`models/huggingface`, `models/gguf`
empty on the SSD). Provisioning needs real downloads (`huggingface-cli` for the
MLX models, the nomic GGUF) — e.g. `provision` a small model (`summary`, ~700MB)
or `text` (3B) for the POC demo. Nothing in `texteditor` blocks this.

## Then continue

**Track 2 (mandatory): `implementation-sequence-future.md`.** Plan E (deployment
& packaging: standalone daemon, Tauri sidecar, dynamic-port discovery, bind
policy, web target, capability adapter) → Plan F (Tauri editor: openapi-to-rust
client, Vue state, CodeMirror UI). D1 enablement stays deferred unless a trigger
fires.

## Read first (unchanged, in this order)

1. `docs/writing-assistant/plans/implementation-sequence.md` (build order; D2–D5
   now landed) and `implementation-sequence-future.md` (Track 2).
2. `docs/writing-assistant/architecture.md` — arc42.
3. `docs/writing-assistant/contracts/` — `interface.md` (note the §1/§2/§8b
   amendment notes), `module-boundaries.md`, `data-model.md`, `daemon-http.md`
   (mirror), `failure-semantics.md`, `state-machine.md`,
   `concurrency-topology.md`.
4. ADRs `0001–0034` for any module you touch — normative. Router context:
   ADR-0028 (with its Recorded amendments block), ADR-0033 (daemon source
   location). Track 2: ADR-0014, 0021, 0013, 0017 §3, 0023, 0026, 0031, 0034.
5. `docs/writing-assistant/behaviors/*.feature` — Gherkin acceptance contracts.

## Hard constraints (never violate)

- Single static Go binary, no CGO. SQLite via `modernc.org/sqlite`.
- Serving is pure llama.cpp / MLX on Metal (ADR-0030); engine reaches serving only
  via `Fleet → daemon` (ADR-0025/0027); never reads `models.json` or calls `serve.sh`.
- The daemon's source lives in **macos-dev-config** (`cmd/fleetdaemon/`), built
  there to `bin/` (ADR-0033, superseding ADR-0032 §1); `texteditor` holds only
  contract **mirrors** (`daemon-http.md` + fleet-manifest schema) with a drift
  test that fails when the sibling checkout is present and they differ.
- Sealed Go interfaces over pure DTOs (ADR-0016/0027): shared DTOs in `shared/dto`.
- Contract-first (ADR-0002/0017): the OpenAPI spec is authored before any client;
  clients consume generated code, never engine internals. Do not silently extend
  the spec — surface route/shape changes as recorded contract amendments.
- Test at the boundary (Q5): `CGO_ENABLED=0 go test ./...` stays green; client
  code is spec-generated (`bun run gen`), not hand-shaped. `bun test` + `bunx
  tsc --noEmit` green in `client/tui/`.

## Machine note (this Mac)

The internal disk is ~full. Per `macos-dev-config`, dev caches live under
`/Volumes/Ex-SSD/caches/<tool>` (managed by `tools/dev-cache.sh`); the bun install
cache was relocated there (`caches/bun`, symlinked from `~/.bun/install/cache`).
Keep TUI cache writes on the SSD; `caches/npm` is the npm cache (`~/.npmrc`).
