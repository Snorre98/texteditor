# Handoff — Phase C5: Reasoning policy + context-window budgets (ADR-0051)

Prompt for a fresh session. Repo: `~/Documents/Liv/Projects/texteditor`
(real path `/Volumes/Ex-SSD/Documents/Liv/Projects/texteditor`).

Status: Phases A–C (C1–C4) landed and green. Phase D may be in flight; Phase E1
(no contract changes, no engine changes) may also be running in parallel — do
not touch `client/tui-rs/`, `client/tui/`, or `client/tauri/`. Do not commit
unless asked.

## Read first (in order)
1. `docs/writing-assistant/adr/0051-reasoning-policy-context-window-budgets.md`
   — the spec you implement (§1–12).
2. `docs/writing-assistant/behaviors/context-budgets.feature` — normative;
   map tests to its scenarios.
3. `docs/writing-assistant/adr/0024` (thinking attribution), `adr-0044`
   (snapshots/inspector), `adr-0045` (one pipeline; no preset fields),
   `adr-0049` (session policy + `Task.context`; pins never bypass budgets),
   `adr-0011` (assembler choke point), `adr-0022` (measurable Q1).
4. `docs/writing-assistant/plans/implementation-sequence-context-engine.md` —
   Phase C5 only.
5. Current code: `server/internal/{provider,loop,assembler,meter,pipeline,session}`,
   `server/config/pipeline.json` + `schemas/pipeline.schema.json`,
   `server/shared/dto/*`, `api/openapi.yaml`, `server/cmd/texteditor/main.go`.
6. `server/internal/provider/provider.go` note: the `defaultMaxTokens = 4096`
   fix (commit `94e30c4`) — do not regress it; thinking budgets interact with it.

## Scope
Phase C5 only. No client work, no Phase D/E/F work. All contract additions are
additive and OpenAPI-first.

## Pinned decisions (from ADR-0051; do not relitigate)
1. **Thinking policy** levels `off|auto|on`. Default from
   `config/pipeline.json`; a session policy override + optional per-turn
   `Task.context` override ride the ADR-0049 shape; never a preset field
   (ADR-0045).
2. **`auto` is bounded escalation**: run thinking-off first; on a structured
   failure (`invalid-structure`, `guard-failed`, or no tool call and no answer)
   retry **once** with thinking-on, labeled `thinking-escalated`, bounded by the
   pipeline step cap. Record the escalation in the snapshot.
3. **Runner mapping** with labeled degradation: mlx-lm per-request
   `chat_template_kwargs: {"enable_thinking": false}`; llama.cpp
   `chat_template_kwargs` where jinja templates are enabled; OpenAI-compatible
   `reasoning_effort`/vendor fields. Unsupported runner → thinking-on with a
   labeled `thinking-unsupported` outcome, never a silent pretend.
4. **Exact thinking accounting + progress**: the provider surfaces reasoning
   deltas as a raw `reasoning` event; the loop accumulates; the meter
   attributes exactly to `thinking` (ADR-0024 approximation only when the
   provider omits the count). A `thinking` SSE event exposes progress. The
   meter's component sum still equals provider-reported totals (Q1).
5. **Thinking budget**: a reasoning-token cap; hitting it emits a labeled
   `thinking-truncated`. A turn that ends `length` with neither a tool call nor
   an answer is a labeled error, never an empty `done`.
6. **Per-turn context-window gate** (assembler, before any provider call):
   `system + tools + pinned/explicit + user + history + RAG + mentions +
   output reserve ≤ model.capabilities.contextLength`. Priority order:
   system+tools+user fixed (never dropped) → pins/explicit → recent history →
   RAG → older history. Drops use the existing `ContextDrop` shape with reason
   `context-window`, counted and labeled. Fixed+pinned+user alone over the
   window → typed `context-window-exceeded` before the provider call.
7. **Session budget live**: soft ratio → labeled `session-budget-soft` warning,
   turn proceeds; hard threshold → typed `session-budget-exceeded` refusal
   unless compaction succeeds first.
8. **Metered compaction**: replace the oldest turns with one labeled summary
   (its own meter row, thinking-off, bounded); pins and recent turns survive;
   the snapshot records `compacted` + the summarized range. Retention follows
   the ADR-0044 note.
9. **Measurements first-class**: every turn records prompt/thinking/completion
   tokens, wall-clock latency, model + quant, window utilization with the
   snapshot/meter.
10. **Data over code**: `config/pipeline.json` + schema gain `thinking`,
    `reserveOutputTokens`, `sessionBudgetSoftRatio`, and `compaction
    {enabled,triggerHistoryTokens,keepRecentTurns}` — validated fail-fast
    (`pipeline-invalid`).
11. **Contract** (OpenAPI-first, additive): `thinking` event; typed
    `context-window-exceeded` / `session-budget-exceeded`; snapshot/meter fields
    (exact thinking, window utilization, `compacted` range,
    escalation/truncation labels); session policy + `Task.context` gain the
    thinking level. Regenerate ogen + Hey API/Zod; the Tauri Rust tree stays
    untouched (tui-rs regenerates in E2).
12. Keep all existing invariants: one pipeline, assembler as the single
    metered choke point, labeled failures only, pins never bypass budgets.

## Checkpoints
- **C5a — thinking policy + accounting + event + measurements**:
  pipeline/session/turn policy plumbing; runner mapping; `auto` escalation;
  exact reasoning accumulation; `thinking` event; `thinking-truncated`; the
  `length`-without-outcome labeled error; per-turn measurements.
- **C5b — window gate + budgets + compaction**: assembler window gate with
  `context-window` drops and the typed refusal; soft/hard session budget;
  metered compaction row + snapshot labels; `pipeline.json` fields + schema.

If the session runs short, stop after a fully green checkpoint and report.

## Tests (required; map to `context-budgets.feature`)
- Provider: thinking toggle mapping per runner; unsupported → labeled degrade;
  reasoning delta surfacing; `defaultMaxTokens` regression guard.
- Loop: `auto` escalation fires once and is labeled; no escalation on success;
  `off` sends the toggle; truncated thinking is labeled; length-without-outcome
  is an error.
- Assembler: window-gate priority drops (recent history → RAG → older
  history); `context-window` drop records; typed refusal when fixed+pinned+user
  overflow; budget sum invariant preserved.
- Meter: exact thinking attribution; soft/hard budget labels; compaction row
  is a separate metered model row; measurements recorded.
- Session/policy: thinking level persists and merges replace-when-present with
  `Task.context`; per-turn override never persists.
- HTTP E2E (real stores, temp dirs, stub provider): over-window refusal;
  over-budget refusal; compaction path; `thinking` event on the stream.

## Verify
```
cd server && CGO_ENABLED=0 go test -count=1 ./...
cd server && CGO_ENABLED=0 go vet ./... && gofmt -l server/
cd server && go generate ./...            # ogen (after spec changes)
cd client/tui && bun run gen              # Hey API + Zod (frozen TUI keeps compiling)
cd client/tui && bun test && bun run typecheck
```
Rust/Tauri untouched.

## Report
Files changed, tests added (mapped to `context-budgets.feature`), exact
commands run, pinned-decision deviations with rationale, and what E2 needs to
know: the `thinking` event payload, the new snapshot/meter fields, the session
policy thinking field, and the typed error codes so the TUI renders them.
