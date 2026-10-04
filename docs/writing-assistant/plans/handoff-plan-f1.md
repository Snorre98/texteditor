# Handoff — Plan F.1 (Laya decision layer): graded enablement + bounded inputs

You are implementing the writing-assistant engine from a locked architecture. The
ADRs and contracts are the source of authority; the implementation plan is the
build order. Read before writing any code, and never silently contradict an ADR.

## Where we are

**Phase F is landed and committed (`619a772`, ADR-0053).** The engine has a sealed
`internal/laya` client that calls Laya's native typed-question API — planner
(`POST /v1/systemone`) before retrieval, gate (`POST /v1/systemone/batch`) after
exclusion — under one global `config/pipeline.json` block (default off), fail-open
and labeled, metered via `AttributeDecision`, recorded as a typed
`ContextSnapshot.decision`, with `GET /decision` and a TUI decision panel/toggle.

Two things Phase F did **not** do, and this handoff is them:

- **Graded enablement** — the single `enabled` boolean runs planner and gate
  inseparably, so the gate (the novel behavior ADR-0054 sanctions as an
  experiment) cannot be enabled or evaluated on its own.
- **Bounded inputs** — the planner state and each gate passage are sent
  unbounded, and Laya truncates to its encoder window (512 for `english`, 1024
  otherwise) **silently**, violating Q1 and scoring passages the model never saw.

The two new ADRs are normative:

- **ADR-0054** — the layer is an *optional, experimental dependency* with a
  normative decoupling guarantee and measured exit criteria.
- **ADR-0055** — exactly the mechanics this handoff lands: `mode` grades the
  policy, and the caps bound the inputs.

### Landed (do not redo)

- `internal/laya` client (planner + gate, native API, fail-open classify).
- `loop.decide` / `loop.gate`; `DecisionRecord` in the snapshot; `decision` meter
  component; `GET /decision`; `ContextPolicy.decision` (`off|on`) + TUI toggle.
- `research/decision-eval.md` (method) + `server/testdata/decision-golden/`
  (fixture). The live sweep is F.2, not this handoff.

### Not present in this workspace

- The **Laya runner** (`serve-laya.sh`, manifest entry) lives in the separate
  **`macos-dev-config`** repo (`~/Projects/macos-dev-config`), not here. Until it
  is up and fleet-resolvable, enabling decision yields `degraded: unreachable`
  and the turn runs ungated. **F.1 does not need the runner** — the tests are
  stubbed — and does not change Laya's native HTTP contract (see F.3).

## Settled (do not re-ask)

1. **Modes, explicit** — `decision.mode: off | planner | planner+gate`, default
   `off`. `ContextPolicy.decision` carries the **same three literal values**
   (absent = inherit). `planner` runs only the planner (retrieve-or-not,
   breadth→topK, thinking); the gate is skipped and every post-exclude candidate
   chunk enters the prompt corpus. `planner+gate` is the full ADR-0053 behavior.
2. **Bounding is engine-side, deterministic, labeled.** Use the ADR-0051 token
   estimator; truncate the planner state to `maxPlannerTokens` and each gate
   passage to `maxChunkTokens`; set `DecisionPlanner.truncated` /
   `DecisionChunk.truncated`. Never rely on Laya's window; never clamp silently.
3. **Contract-first lockstep** — `api/openapi.yaml` first, then `go generate ./...`
   (ogen) **and** the TUI Rust regen (`openapi-to-rust`). `client/tauri` and
   `client/tui` stay frozen (ADR-0044). Never hand-shape generated files.
4. **`off`/nil is a behavioral no-op** vs the pre-Phase-F pipeline (ADR-0054 §2):
   same retrieval depth (`autoRagTopK`), same payload, no `DecisionRecord`, no
   `decision` meter rows, no gate drops. Asserted by test.
5. **Metering unchanged** — `planner` writes one row; `planner+gate` writes two
   (ADR-0053 §9).
6. **Renaming `enabled` → `mode` is a breaking config/contract change, accepted**
   (ADR-0055 §4). Old configs fail startup `schema-invalid` — intended.

## Work — F.1 (engine/code; no runner required)

### A. Contract — `api/openapi.yaml` (do this first)

1. `DecisionPolicy`: remove `enabled`; add `mode` (`enum: [off, planner,
   planner+gate]`) and `maxChunkTokens`, `maxPlannerTokens` (integer ≥ 1). Update
   the `required` list. Rewrite the `GET /decision` operation description.
2. `ContextPolicy.decision`: enum `[off, planner, planner+gate]` (was `[off, on]`).
   Update the description (per-turn > session > global; `off` = zero calls).
3. `DecisionPlanner` and `DecisionChunk`: add `truncated: boolean`.
4. Regen: `cd server && go generate ./...`; TUI `openapi-to-rust` per
   `client/tui-rs/` config. Commit spec + generated together.

### B. Config + schema

5. `config/pipeline.json`: `decision.mode: "off"`, add `maxChunkTokens: 384` and
   `maxPlannerTokens: 1024`. Keep `model`, `gateThreshold`, `maxCandidates`,
   `maxHistoryTurns`, `breadthTopK`, `timeoutMs`.
6. `config/schemas/pipeline.schema.json`: `mode` enum replaces `enabled`; add the
   two caps (integer, minimum 1); update `required`; `additionalProperties` stays
   false so an old `enabled` key fails loudly.

### C. DTOs (`server/shared/dto/pipeline.go`, `context.go`, `decision.go`)

7. New `DecisionMode` type + constants (`DecisionModeOff`, `DecisionModePlanner`,
   `DecisionModePlannerGate`); `DecisionPolicy.Mode DecisionMode` replaces
   `Enabled bool`; add `MaxChunkTokens`, `MaxPlannerTokens int`.
8. `DecisionOverride` gains the same three values (replace `DecisionOff`/`DecisionOn`
   — keep `off`; add `planner`, `planner+gate`).
9. `DecisionPlan` and `DecisionChunkResult`: add `Truncated bool`; thread it into
   the wire `DecisionPlanner`/`DecisionChunk` via the snapshot record.

### D. Pipeline loading (`server/internal/pipeline/pipeline.go`)

10. Parse `mode` (validate against the enum) + the two caps; fail-fast
    `ErrInvalid`/`schema-invalid` on unknown mode or cap < 1. Drop the `Enabled`
    mapping.

### E. Engine — `server/internal/loop/loop.go`

11. `decide`: resolve effective mode = per-turn override > session > global.
    `off` → return early with no record (no calls). `planner` and `planner+gate`
    run the planner as today. Carry the effective mode in `decisionResolution`.
12. Gate invocation: run `l.gate` only when effective mode == `planner+gate`
    **and** `dres.retrieve`. `planner` keeps all candidates.
13. Add a deterministic decision-input truncator (reuse the ADR-0051 estimator):
    planner state → `maxPlannerTokens`; each gate passage → `maxChunkTokens`.
    Set `truncated` on the affected records (`plan.Truncated`,
    `chunkResult.Truncated`), and make the text handed to the client post-clamp.
14. `breadthTopK`/`maxCandidates` logic unchanged; `off` still uses
    `policy.AutoRagTopK`.

### F. Laya client (`server/internal/laya/client.go`)

15. `plannerState`/`gateState` consume the already-clamped text; surface
    `Truncated` from the input into `DecisionPlan`/`DecisionChunkResult`. The
    client stays transport-only; bounding lives engine-side.

### G. Tests

16. **Baseline no-op**: `mode=off` and `l.Decision==nil` → zero calls, no
    `DecisionRecord`, topK == `autoRagTopK`, full pre-gate candidate set
    (ADR-0054 §2).
17. **`planner` mode**: gate never called; all candidates kept; planner still sets
    retrieve/breadth/thinking; one meter row.
18. **`planner+gate`**: existing keep/drop + `gate` drop label; two meter rows.
19. **Truncation**: over-long planner state and passage set `truncated` and the
    text is clamped deterministically.
20. **Override precedence**: per-turn enum > session > global; `off` at any layer
    → zero calls.
21. Update the existing decision tests (`TestDecisionGateRecordsAndMeters`,
    `TestDecisionDegradedLabeled`, `TestDecisionRetrieveFalseSkipsRetrieval`,
    `TestDecisionDisabledRunsNoCalls`, `TestDecisionOverrideOffDisables`,
    `TestDecisionPinsBypassGate`) and the TUI toggle test to the three values.

### H. Docs lockstep

22. `contracts/data-model.md` — pipeline `decision` row → `{mode, model,
    gateThreshold, maxCandidates, maxHistoryTurns, breadthTopK, maxChunkTokens,
    maxPlannerTokens, timeoutMs}`.
23. `contracts/interface.md` — `DecisionPolicy`/`ContextPolicy` shapes.
24. `status.md` — F.1 landed + gate evidence; note the runner still absent.
25. `research/decision-eval.md` — replace the `enabled: false` default line with
    `mode: off`; add the caps to the provisional defaults.
26. `architecture.md`/`traceability.md` rows 0054/0055 already indexed; no change
    unless the generated shape differs.

## Work — F.2 (experiment; needs the runner — do not start here)

27. Replace the golden fixture with the thesis-scale (60–80 page) corpus + the
    author's judged queries; sweep `gateThreshold` × `breadthTopK`; compare
    checkpoints (`english`/`multilingual`/`typed-decisions`) and exports
    (fp32 vs ONNX int8). Record recall@k, injected tokens, gate latency, keep
    rate. Set shipped defaults from the sweep and retire ADR-0054's "unproven"
    caveat (or take the fine-tune path / turn the layer off).

## Work — F.3 (cross-repo, `macos-dev-config`) — documentary here

28. **F.1 does not change Laya's native HTTP contract**, so the runner should need
    no change for F.1. Verify in `~/Projects/macos-dev-config`:
    - `serve-laya.sh` + manifest entry expose `serve/start/stop/status/log` with
      the daemon `HOST`/`PORT` seam (mirror `serve-needle.sh`);
    - the `laya` Fleet name resolves and `/health` backs the daemon `status`;
    - the wrapper passes the per-call `max_len`/`max_chunk` budget knobs through
      (if it pins any), consistent with ADR-0055 §3.
    If that repo pins a request/response schema for Laya, mirror it against
    ADR-0053 §3/§4 (the typed API is stable) and flag drift — the runner is out
    of this workspace and cannot be tested here.

## Ordering (mandatory)

1. **A** contract + both codegens (lockstep, one commit).
2. **B/C/D** config/schema/DTOs/pipeline.
3. **E/F** engine resolution + truncation.
4. **G** tests (baseline no-op first).
5. **H** docs; then **Gate**; commit "Phase F.1 — graded decision enablement +
   bounded inputs (ADR-0055)".

## Verification gates

- `cd server && CGO_ENABLED=0 go test -count=1 ./...` green; `CGO_ENABLED=0 go
  vet ./...` clean; `gofmt -l server/` clean.
- `client/tui-rs`: `cargo test` + `./tools/build-tui-rs.sh --test` green. (Known
  caveat: `cargo fmt --check` is environment-red on generated block-doc delimiters
  under rustfmt 1.9 — HEAD fails identically; do not treat it as a regression.)
- `client/tui` and `client/tauri` untouched (frozen, ADR-0044) — except that
  `client/tui-rs` is regenerated from the amended spec.
- Behavior: decision scenarios in `context-inspector.feature` (outcomes recorded
  and metered; degradation labeled; no silent truncation) assert as loop/meter
  tests.

## Read first (in this order)

1. This handoff; then
   [`implementation-sequence-context-engine.md`](implementation-sequence-context-engine.md)
   Phase F.
2. **ADR-0053** (the as-built mechanism) — normative.
3. **ADR-0054** (optional/experimental + decoupling) and **ADR-0055** (graded
   mode + bounded inputs) — normative; these are what this handoff implements.
4. `contracts/interface.md` (`DecisionPolicy`, `ContextPolicy`,
   `ContextSnapshot.decision`), `contracts/data-model.md` (pipeline policy,
   `decision` meter component), `contracts/failure-semantics.md`
   (`decision-degraded`).
5. `research/decision-eval.md` (F.2 method) + `server/testdata/decision-golden/`.

## Hard constraints (never violate)

- **Decoupling guarantee (ADR-0054 §2)**: the engine boots and every turn
  completes with the layer `off` or Laya absent; no startup resolve; fail-open +
  labeled; `off` is a no-op vs pre-Phase-F.
- **Q1 (ADR-0011)**: no silent truncation on the decision path — every clamped
  input is labeled `truncated`.
- **Contract-first (ADR-0002/0017)**: spec first, then both codegens in lockstep;
  never hand-shape generated files.
- **One global policy (ADR-0045 §7)**: no per-preset decision fields.
- **Human overrides outrank Laya (ADR-0049 §11, ADR-0053 §7)**: pins/mentions/
  anchor bypass the gate; explicit `autoRag:false`/`thinking` still win.
- Pure-Go engine, no weights embedded (ADR-0003); Laya only behind Fleet.

## Report back

At each milestone: what landed, which tests pass, and any place the docs forced a
stop or a judgment call. Specifically flag: (a) the exact `mode`/cap values chosen
and whether the pipeline schema rejects the old `enabled` key with a clear error;
(b) how `truncated` is surfaced end to end (planner + per-chunk) and that no path
clamps silently; (c) the `off`/nil baseline test proving the decoupling
guarantee; and (d) the cross-repo F.3 findings (or that `macos-dev-config` could
not be reached).
