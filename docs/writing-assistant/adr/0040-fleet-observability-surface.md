# ADR-0040: Fleet observability surface — model selection shows serving availability

Status: Accepted

## Context

The model selection must show which serving services are available: the GUI
(Tauri/web) has **no model selector at all** today, the TUI switcher renders
`liveState` once at mount and never refreshes, and the engine's `GET /models`
does N+1 sequential daemon `status` calls while surfacing a daemon outage as a
raw 500. PresentationToMarkdown already has the pattern this project is
missing (`/api/health/servers` + per-server up/down + refresh), but here the
engine can do better than a static hint: it owns the lifecycle verbs
(`start`/`stop`/`provision`), so the observability surface can show state *and*
offer the remediation.

Forces:

- Serving is reachable **only** through the control daemon (ADR-0025/0027);
  the observability surface must not read `models.json` or probe runner ports
  directly.
- The engine's module discipline (ADR-0016): the Fleet gateway is the ONLY
  engine reach into serving; the API server adapts it; clients are dumb
  (ADR-0013 §3) — so batch status and last-good retention live engine-side,
  not client-side.
- `daemon-unreachable` must stay distinct from `provider-unreachable`
  (failure-semantics §6): a dead control plane is a fleet-level fact, not a
  per-model one.
- Contract-first (ADR-0002/0017): the OpenAPI spec changes before any client
  code; every wire shape is a recorded amendment.

## Decision

1. **Batch state verb.** The daemon gains `GET /status/all`
   (macos-dev-config ADR-0007): every model's state in one roundtrip, manifest
   order, `unknown → down` folded exactly like the single verb, no
   provisioning progress. The Fleet gateway gains
   `ListStatus() ([]dto.ModelState, error)` over it, where
   `ModelState{Name string, State LiveState}`.

2. **One observability route: `GET /fleet`.** Returns
   `FleetState{control: "up" | "unreachable", models: FleetModel[]}` where
   `FleetModel` is the existing `Model` schema plus `liveState`. The handler
   joins `list` + `status/all` (two daemon calls total, no N+1).

3. **Daemon outage is data, not an error.** When the daemon is unreachable,
   `GET /fleet` answers 200 with `control: "unreachable"` and the **last-known
   model projection** (every model's `liveState` forced to `unknown`). The
   last-good list is an in-memory cache in the Fleet gateway, replaced on
   every successful `list` — never persisted, never a file read. This mirrors
   PtM's fleet.py last-good cache and keeps the clients stateless.
   `GET /models` stays as-is (discovery; retained for compatibility, marked
   deprecated in the spec description); clients move to `/fleet`.

4. **Client refresh policy: manual + interval poll.** Clients fetch `/fleet`
   on mount, on demand ("refresh"), and every 10 s while visible (paused when
   the window is hidden), plus immediately after any lifecycle verb
   (`start`/`stop`/`provision`/switch). No SSE push — polling at 10 s over a
   machine-local socket is cheaper than a new transport and its connection
   lifecycle; revisit only if poll latency becomes observable.

5. **Remediation affordances.** The model selector renders per-model state
   (`up`/`down`/`starting`/`provisioning`/`unknown`) and offers the verb the
   state implies: **Start** on `down`, **Stop** on `up` (when not the active
   selection), a **provision hint** when a start fails with `model-not-found`,
   and a distinct **control-plane banner** when `control: "unreachable"`.
   Actions route through the existing `POST /models/{name}/start|stop` verbs —
   no new write surface.

## Consequences

- **+** One screen of fleet state costs two daemon calls instead of N+1; a
  daemon outage renders as a labeled banner, not a broken selector or a 500.
- **+** The GUI finally surfaces what the store already holds (models +
  `liveState`), closing the observability gap behind the mode selector.
- **−** Fleet state is a snapshot: up to one poll interval stale. Accepted —
  state transitions (starting → up) already take seconds and clients refresh
  after every action they issue.
- **−** The last-good cache can serve a stale projection during an outage —
  it is explicitly labeled (`control: "unreachable"`, all states `unknown`),
  never silent (Q3 discipline, ADR-0022).
- **−** `/fleet` and `/models` overlap until clients migrate; `/models` is
  retained for compatibility and deprecated, not removed (recorded amendment
  to ADR-0017 §4).

## Alternatives considered

- **Engine-side N+1 aggregation without a daemon change** — rejected: keeps
  the roundtrip cost and progressive-paint problem and duplicates aggregation
  in every consumer (see macos-dev-config ADR-0007).
- **Extend `GET /models` with an envelope** — rejected: breaking change to an
  existing array response; a new `/fleet` route lets clients migrate without
  a flag day.
- **SSE push of fleet state** — rejected: new transport + eventbus plumbing
  for a problem 10 s polling solves; the `/turn` SSE stream stays the only
  streaming surface (ADR-0031).
- **Client-side last-good retention** — rejected: violates the dumb-client
  principle (ADR-0013); the engine owns the fleet projection, clients render
  it.
- **No retention (empty models on outage)** — rejected: the selector would
  vanish exactly when the user needs to see what *was* available; the labeled
  stale projection is more useful and still honest.

## Recorded note (2026-09-07) — client-side fleet orchestration is duplicated; move it engine-side

*Observed after landing this ADR, recorded for the next ADR rather than
amending this decision:*

Each client store now owns the same fleet orchestration logic — `startModel` /
`stopModel` (busy flag, action-error capture with the provision hint, refresh
after the verb), `startFleetPoll` / `stopFleetPoll` (interval, in-flight guard,
visibility pause), and the `FleetView` slice (`control`/`models`/`error`/`busy`).
The TUI (Solid) and the Tauri/web editor (Vue) carry nearly identical copies of
this — client-side duplication of *engine-domain* behavior.

The engine is the natural owner of this: it already owns the last-good
projection and the lifecycle verbs. If the engine exposed exactly what the
selectors need — e.g. a fleet state-change feed (state transitions pushed or
observed engine-side) and/or a higher-level "switch" surface that folds in
refresh semantics — the clients would shrink to rendering only, per the
dumb-client principle (ADR-0013 §3) this ADR already relies on.

**Future work (separate ADR):** move fleet orchestration into the engine and
have clients consume it, eliminating the duplicated store logic in both
clients. Not a defect in the current contract (`/fleet` + per-model verbs work
as specified); the duplication is the symptom.

