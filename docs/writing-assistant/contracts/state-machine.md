# State machine contracts

Explicit states and transitions for the stateful units of work. Source ADRs:
ADR-0002 (loop), ADR-0007 (lifecycle), ADR-0016 (loop is thin; turn is async),
ADR-0018 (async provision), ADR-0026 (sessions).

## 1. Agent-loop turn

One turn = task → plan → dispatch tool(s) → observe → repeat → answer. Originated
by `Loop.Run(ctx, task) → (turnID, err)` (async, ADR-0016); every event carries
`turnID`. A turn is **scoped to a session** (`task.SessionID`, ADR-0026); the loop
reads the session's `History` into the assembler and appends each turn's messages
back to the session.

### 1.1 States

| State | Kind | Meaning |
|---|---|---|
| `idle` | phase | no turn in progress |
| `planning` | phase | building the plan for the task |
| `deciding` | phase | resolving a `request_tool` intent to a concrete tool (router mode only) |
| `dispatching` | phase | a tool call is in flight |
| `observing` | phase | integrating tool results |
| `answering` | phase | streaming the final answer |
| `done` | terminal | turn completed |
| `error` | terminal | turn failed |

### 1.2 Transition table

| From | To | Trigger | Notes |
|---|---|---|---|
| (start) | `planning` | `Run(task)` | |
| `planning` | `dispatching` | plan needs a tool | else → `answering` |
| `planning` | `deciding` | writer emitted `request_tool` (router mode) | native mode skips straight to `dispatching` |
| `deciding` | `dispatching` | `Decide` returned `Confidence ≥ τ` | |
| `deciding` | `answering` | `Confidence < τ` / refusal / transport error | graceful "no tool" |
| `dispatching` | `observing` | tool returned | via Tool executor `Invoke` |
| `observing` | `dispatching` | plan needs another tool | bounded by `mode.maxSteps` |
| `observing` | `answering` | plan complete | |
| `answering` | `done` | stream finished | emit `done` (with `degraded`/`usedModel`) |
| any | `error` | unrecoverable failure | emit `error` |

### 1.3 Invariants

- `mode.maxSteps` bounds the dispatch/observe cycle (bounded per mode, not a global;
  ADR-0019). A non-`agentic` mode (`agentic: false`) has `maxSteps=0`: single-shot,
  no tool loop.
- `deciding` exists only when `mode.toolCalling == "router"`; the native path's
  `planning → dispatching` is unchanged (ADR-0028).
- `answering` is entered exactly once per turn; after `done`/`error` the loop
  returns to `idle`.
- One turn in flight **per session**; distinct sessions run turns concurrently
  (ADR-0026). Within a session, at most one turn runs at a time.

## 2. Serving lifecycle (per model server / daemon)

Owned by the control daemon (ADR-0025); observed by Fleet via `Status`.

### 2.1 States

| State | Kind | Meaning |
|---|---|---|
| `down` | phase | not running |
| `starting` | phase | launched, awaiting health |
| `up` | condition | answering `/health` (or `/v1/models`, `/api/tags`) |
| `stopping` | phase | `stop` issued |
| `provisioning` | phase | weights being fetched (HF download, async) |

### 2.2 Transition table

| From | To | Trigger | Notes |
|---|---|---|---|
| `down` | `starting` | `start <name>` | refuse if port busy |
| `starting` | `up` | health check passes | within 60s, else warn + stay `starting` |
| `up` | `stopping` | `stop <name>` | |
| `stopping` | `down` | process exited | |
| `down` | `provisioning` | `provision <name>` (async) | returns `provisionID`, 202 |
| `provisioning` | `down` | download complete/failed | then `start` (or error) |

### 2.3 Invariants

- `start` on `up` is an error unless `status` reports `up` (idempotency).
- `stop` on `down` is a no-op warning.
- Port conflicts are detected *before* launch, never mid-launch.
- Provision progress (`bytes`/`total`) is observable via `status` in the
  `provisioning` state (ADR-0018).

## 3. Model resolution (Fallback)

`Fleet.Resolve` (ADR-0016) is stateless-with-respect-to-this-state-machine but
drives the degrade path:

- `Resolve` reads the current `LiveState` of the preferred model and, if `down`/
  `not-found`, walks `modeTag` candidates by fleet policy (ADR-0015), selecting the
  first `up` (marks `Degraded=true, UsedName=<fallback>`).
- No `up` candidate → the caller emits `error` with `no-model-available`.

## 4. Checkpoint alignment

- Every agent-loop transition persists its `meter_events` (via `Meter.Attribute`)
  and any document mutation (git commit) before the next step begins (ADR-0004).
- AI edits stage a candidate and commit on accept; manual edits autosave on a
  silence interval (ADR-0020).

## 5. Session (ADR-0026)

A Session is a persisted entity; it has no property state machine beyond
create/resume, but its relationship to turns is contractual:

- A session owns a message history (`messages`, `sessions.db`) and a cumulative
  token tally (`meter_events.session_id`).
- `SessionStore.Resume(id)` / `Create(documentID, anchorBlockID, modeType)` is
  **create-or-resume**: a `(document_id, anchor_block_id)` pair maps to at most
  one session.
- A session's `TokenBudget` (optional) is checked by the Meter each turn; a turn
  that would cross it surfaces `session-budget-exceeded` (a visible lever, not a
  hardcode).
