# Handoff — fleet observability (ADR-0040)

You are implementing the fleet observability surface: model selection must show
what serving services are available, with the state the daemon reports and the
remediation the state implies. Read the ADRs and contracts before writing code;
never silently contradict an ADR.

## Where we are

**Engine + daemon + contract side landed; the client surfaces are next.**
ADR-0040 (fleet observability) and macos-dev-config ADR-0007 (batch status
verb) are Accepted. The engine surface and daemon verb are implemented and
tested; the GUI model selector, TUI parity, and the client regen/verification
are this session's work.

### Landed + tested (engine + daemon + docs — committed in two repos)

- **macos-dev-config**: `GET /status/all` batch verb (`fleetdaemon.Control.StatusAll`,
  `dto.StateEntry`, `http.go` handler branch), daemon tests
  (`TestStatusAllMatchesSingleVerb`, `TestHTTPStatusAll`), canonical
  `docs/contracts/daemon-http.md` §2 amendment, ADR-0007 + index row.
- **texteditor engine**: `Fleet.ListStatus()` over `/status/all`; last-good
  projection cache (in-memory, mutex-guarded, replaced on every successful
  `list`) served with the wrapped `ErrDaemonUnreachable` on outage —
  `ListModels`/`ListStatus` only; `Resolve`/`Status` keep hard-fail
  (`fleet.go`, fleet tests incl. `TestListModelsLastGoodOnDaemonDown`,
  `TestResolveUnaffectedByStaleCache`, `TestListStatusBatch`).
- **API**: `GET /fleet` → `FleetState{control: up|unreachable, models:
  FleetModel[]}` (`FleetModel` = Model + required `liveState`); `/models` kept
  and marked deprecated; genapi regenerated; `apiserver.GetFleet` +
  `fleetSnapshot` join (list + status/all = two daemon calls); `ListModels`
  handler switched to the batch status; apiserver tests (`TestGetFleet`,
  `TestGetFleetDaemonUnreachable`).
- **Docs**: ADR-0040, ADR-0017 recorded amendment, `interface.md` §1
  (`ListStatus`, `ModelState`, last-good semantics), `module-boundaries.md`
  Fleet row, `failure-semantics.md` §5 read-surface row, mirror-synced
  `daemon-http.md`, `behaviors/fleet-observability.feature`,
  `traceability.md` row, `status.md` snapshot.

Verification so far: `CGO_ENABLED=0 go test -count=1 ./...` green (both repos);
contract-mirror drift test passes.

## Still open (explicit list; do not silently choose)

### C1 · GUI store (client/tauri/src/state/store.ts)

Replace the models slice with a fleet slice:

```ts
fleet: { control: "up" | "unreachable"; models: FleetModel[] }
```

- `refreshFleet()` → `getFleet()`; keep `refreshModels()` as an alias or drop
  its users (they move to fleet).
- Polling: `setInterval` 10 s, **paused on `document.hidden`** (visibilitychange
  listener); one in-flight guard; manual `refreshFleet` exposed for the UI.
- Actions: `startModel(name)` / `stopModel(name)` calling the existing verbs,
  then an immediate `refreshFleet()`; generalize `switchModel` over them.
- Daemon outage: `control: "unreachable"` + models render `unknown` — the
  selector stays visible (the engine already sends the last-good projection).
- Errors: lifecycle-verb failures surface in a UI-visible slice (reuse
  `connection.error` or a dedicated `fleetError`).

### C2 · GUI model selector (client/tauri/src/editor/Editor.vue + useAssistant.ts)

Next to the mode selector in the chat header:

- One row per model: name + status dot (`up` green / `down` red / `starting` &
  `provisioning` amber pulsing / `unknown` gray) + `baseUrl` muted.
- Actions per state: **Start** on `down`/`unknown`; **Stop** on `up` (when it
  is not the currently selected model); disabled while a verb is in flight
  (show `starting…`).
- Provision hint: a failed `start` with `model-not-found` renders
  "model not provisioned — see macos-dev-config/models.json" (mirror
  `useAssistant.PROVISION_CODES` style).
- Control-plane banner: when `fleet.control === "unreachable"`, a distinct
  banner "serving control unavailable — showing last known models" above the
  selector.
- Manual refresh button (mirrors PtM's "Check servers").

### C3 · TUI parity (client/tui/src)

- `state/store.ts`: fleet slice (`{control, models}`) over `getFleet()`;
  `startModel`/`stopModel` actions + refresh; 10 s poll in `app.tsx` (Solid —
  `setInterval` in an effect, paused when the app is backgrounded is optional
  here; keep the interval simple).
- `ui/switcher.tsx`: render per-model state colors/actions + control banner;
  keep the existing start-new-then-stop-old `switchModel` flow on selection.

### C4 · Regen + verification

- `bun run gen` in `client/tui` and `client/tauri` (Hey API) — the spec gained
  `/fleet`; the Rust sidecar only consumes `/health`, so
  `openapi-to-rust` regen is **only** needed if the shared types file diffs —
  verify with `cargo check` in `src-tauri`.
- `bun test` + `bunx tsc --noEmit` green in both clients; `cargo test` green.
- Manual: engine + daemon up → toggle a model server externally → the selector
  reflects it within one poll; stop the daemon → banner + unknown states,
  no 500.

## Verification gates

- `CGO_ENABLED=0 go test -count=1 ./...` (texteditor) + `go test ./...`
  (macos-dev-config) stay green.
- `fleet-observability.feature` scenarios asserted as Go unit tests (already:
  batch join, outage labeling, batch/single agreement) + client store tests
  (poll pause, refresh-after-action, control banner state).
- Contract-mirror drift test passes.
- `bun test`/`tsc` green; `cargo check`/`cargo test` green.

## Read first (unchanged, in this order)

1. `docs/writing-assistant/adr/0040-fleet-observability-surface.md` — normative.
2. `docs/writing-assistant/contracts/interface.md` §1, `failure-semantics.md`
   §5/§6, `docs/writing-assistant/behaviors/fleet-observability.feature`.
3. macos-dev-config `docs/adr/0007-batch-status-verb.md` +
   `docs/contracts/daemon-http.md` §2 (`status/all`).
4. `client/tauri/src/state/store.ts` + `client/tui/src/state/store.ts` (the
   stores to extend) and `client/tui/src/ui/switcher.tsx` (parity target).

## Hard constraints (never violate)

- Serving only via `Fleet → daemon` (ADR-0025/0027); the clients touch only
  the engine's REST surface, never the daemon or `models.json`.
- Clients are dumb (ADR-0013 §3): the engine owns the last-good projection;
  the clients render `control`/`liveState` verbatim and never synthesize state.
- Contract-first: the spec is already amended; regen clients from
  `api/openapi.yaml` (`bun run gen`) — never hand-shape request/response types.
- The 10 s poll pauses when the document is hidden (ADR-0040 §4); no polling
  before the first document/window visibility.

## Then continue

Fleet observability landed → update `status.md` (Tauri UI surface row, TODO
#1's model-selector half) and close out `handoff-tauri-ui.md`'s remaining
items (selection trigger, if still broken).

## Report back

At each milestone: what landed, which tests pass, and any place the docs
forced a stop or a judgment call. Specifically flag: (a) the exact
`FleetModel`/`FleetState` shapes the generated clients produced (they must
match `api/openapi.yaml`), (b) whether the Rust regen was needed, and (c) any
UI copy you invented beyond the ADR (record it, don't invent silently).
