# State machine contracts

Explicit states and transitions for the three stateful units of work. Source ADRs:
ADR-0002 (loop), ADR-0007 (lifecycle), ADR-0016 (loop is thin; turn is async),
ADR-0018 (async provision).

## 1. Agent-loop turn

One turn = task → plan → dispatch tool(s) → observe → repeat → answer. Originated
by `Loop.Run(ctx, task) → (turnID, err)` (async, ADR-0016); every event carries
`turnID`.

### 1.1 States

| State | Kind | Meaning |
|---|---|---|
| `idle` | phase | no turn in progress |
| `planning` | phase | building the plan for the task |
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
| `dispatching` | `observing` | tool returned | via Tool executor `Invoke` |
| `observing` | `dispatching` | plan needs another tool | bounded by `mode.maxSteps` |
| `observing` | `answering` | plan complete | |
| `answering` | `done` | stream finished | emit `done` (with `degraded`/`usedModel`) |
| any | `error` | unrecoverable failure | emit `error` |

### 1.3 Invariants

- `mode.maxSteps` bounds the dispatch/observe cycle (bounded per mode, not a global;
  ADR-0019). A non-`agentic` mode (`agentic: false`) has `maxSteps=0`: single-shot,
  no tool loop.
- `answering` is entered exactly once per turn; after `done`/`error` the loop
  returns to `idle`.

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
