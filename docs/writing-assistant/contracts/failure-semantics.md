# Failure semantics contract

How the system handles each failure class: retry + backoff, degrade rules, and
error surfacing. Source ADRs: ADR-0005/0007/0025 (serving), ADR-0016 (attribution
scale-to-total), ADR-0024 (thinking approximation), ADR-0002 (loop).

## 1. Retry policy + backoff

| Failure class | Retries | Backoff | Never retried |
|---|---|---|---|
| Provider transient (5xx, timeout, connection reset) | 3 | exponential, base 250ms, cap 2s | 4xx client errors |
| Model not loaded / cold-start | 1 (re-check after 1s) | fixed 1s | — |
| Port-in-use on `start` | 0 | — | yes (surfaced immediately with remap hint) |
| HF download interrupted | 3 | resume (HF resumes by content) | auth errors (gated model, no login) |
| Daemon (control) unreachable | 3 | exponential, base 250ms, cap 2s | — |

Retries are **bounded (≤3)**; every failure is recorded in `meter_events`/logs.

## 2. Serving failure

- **Provider down / unreachable** — the Provider gateway surfaces
  `provider-unreachable` to the caller.
- **Model not provisioned** — `Fleet.Resolve` (via the daemon) returns
  `model-not-found`; the caller surfaces `provision-required` (the TUI can offer
  `provision <name>`).
- **Port conflict** — `start` refuses and prints the `SERVE_PORT_<NAME>` override;
  no process is spawned.
- **Lanes conflict** — manifest load fails with `lanes-conflict` naming both entries
  (ADR-0018); this is a startup error, not a runtime one.
- **Router validation** — a `toolCalling: "router"` mode with no resolvable
  `needle-router` model fails startup with `mode-refs-router-unavailable`; a
  `needle-router` whose manifest `source.fingerprint` differs from the engine's
  tool-set hash fails startup with `router-tools-stale` (ADR-0028).

## 3. Degrade-to-partial

- **Model fallback** — when a mode's default model is `down` or `not-found`,
  `Fleet.Resolve` selects the next model sharing the mode's `modeTag`, ordered by
  the fleet policy (ADR-0015), and marks `Resolution.Degraded=true,
  UsedName=<fallback>`. The completion is labeled via the `done` event
  (`degraded`, `usedModel`), never silent.
- **Retriever empty** — zero chunks is *not* an error; the context assembler
  proceeds without RAG and records `rag: 0` tokens.
- **Tool failure** — a failed tool call returns a structured error to the agent
  loop (not a crash); the loop may retry once or continue without the tool.
- **Tool routing (router mode)** — `Decide` resolves the writer's `request_tool`
  intent. Confident (`Confidence ≥ τ`) → dispatch. Low-confidence / refusal /
  empty-call → proceed to `answering` (graceful, not an error). Transport failure →
  labeled `error` (`router-unreachable`) then `answering` (no retry).

## 4. Attribution and the thinking-token approximation

- The Meter scales the assembler's `Breakdown` onto the provider's exact totals
  (ADR-0016) — the scaled sum equals the provider total exactly (Q1, ADR-0022).
- A source that overflows/truncates is reported as a **labeled overflow line**,
  never silently folded.
- Thinking tokens: exact when the provider reports them. When the provider omits
  the count, the Meter tokenizes the reasoning prefix (ADR-0024) and marks the
  result `approx=1` — a **labeled approximation**, never folded into completion.

## 5. Error surfacing to the caller

| Outcome | Caller-visible result |
|---|---|
| provider unreachable | `error` SSE event, code `provider-unreachable` |
| model not found / not provisioned | `error` SSE event, code `model-not-found` / `provision-required` |
| no servable model for the mode | `error` SSE event, code `no-model-available` |
| fallback used | `done` event with `degraded=true, usedModel` |
| tool failed | tool result marked failed; loop continues or aborts per plan |
| port-in-use | daemon/CLI stderr with `SERVE_PORT_<NAME>=<newport>` hint |
| lanes conflict | startup error, `lanes-conflict` + both entries |
| start timeout | `error`, code `start-timeout` (60s bound) |
| unknown tool handler | startup error, `tool-has-no-handler` |
| router mode with no served Needle | startup error, `mode-refs-router-unavailable` |
| router fine-tuned against a different tool set | startup error, `router-tools-stale` |
| router unreachable mid-turn | `error` event, `router-unreachable`, then graceful answer |

## 6. Invariants

- No failure aborts a *turn* unless unrecoverable (provider 4xx, invalid request).
- A model substitution is always labeled; it is never silent (Q3, ADR-0022).
- A token attribution that is approximate is always labeled (`approx=1` / overline);
  it is never silent (Q1, ADR-0022).
- The daemon (ADR-0025) failing is distinct from a runner failing: daemon
  unreachable means *no serving control*, surfaced as `daemon-unreachable`, not
  mistaken for a single-model `provider-unreachable`.
