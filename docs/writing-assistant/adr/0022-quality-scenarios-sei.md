# ADR-0022: Quality goals as measurable SEI general scenarios

Status: Accepted

Supersedes: ADR-0011 (thinking-token attribution measurable target only — see
ADR-0024).

## Context

The top-5 quality goals (§1.2, §10) were prose: "a per-component breakdown is
reported," "behavior changes with no rebuild." They are not testable until each is
stated as an SEI general scenario — source / stimulus / artifact / environment /
response / response-measure — with concrete numbers. This ADR fixes the numbers and
names the tactics; it does not relitigate the goals themselves.

## Decision

### Q1 — Transparent token cost

- **Source / Stimulus:** any turn; the provider's usage (`prompt_eval_count` /
  `eval_count`) lands.
- **Artifact:** the token meter (breakdown + `meter` SSE event).
- **Environment:** local, single-user, live stream.
- **Response:** the Meter scales the assembler's breakdown onto the exact totals
  and emits `meter`.
- **Response-measure:** breakdown rendered ≤ **100 ms** after usage lands; the sum
  of scaled components equals the provider total **exactly** (0 sum-error by
  construction, because the Meter scales onto exact totals). A truncated/overflowed
  source is reported as a **labeled overflow line**, never silently wrong.
- **Tactics:** *separation of responsibilities* (assembler = "what's in each
  component"; meter = "scale to exact total"); *maintain multiple copies* at the
  scene of the failure (raw totals preserved alongside attribution).

### Q2 — Modifiability

- **Source / Stimulus:** an edit to a mode or tool definition or the fleet
  manifest.
- **Artifact:** mode/tool data files; fleet manifest.
- **Environment:** running engine.
- **Response:** the change takes effect on the **next turn** with **0 rebuilds**.
- **Response-measure:** startup load-and-validate ≤ **50 ms** over all mode/tool
  files; a later edit is re-validated on read, failing fast.
- **Tactics:** *data, not code* (ADR-0019); *defer binding* (name-keyed, ADR-0019);
  *use an intermediary* (the fleet manifest as the control panel, ADR-0018).

### Q3 — Hot-swappable serving

- **Source / Stimulus:** the preferred model goes `down` or `not-found`.
- **Artifact:** a turn's model resolution (`Fleet.Resolve` → `Resolution`).
- **Environment:** on-demand fleet (a cold model may need `start` → `up`).
- **Response:** a fallback in the same `modeTag` serves, labeled `degraded=true`.
- **Response-measure:** fallback serves within **≤ 60 s** when cold (inherits
  ADR-0007's health-wait bound); the substitution label is **guaranteed** — no
  `done` event without `degraded=true, usedModel` when a non-preferred model
  serves (tested, not best-effort).
- **Tactics:** *monitor* (live `Status`); *reintroduce a good version* (fallback
  ladder by fleet policy, ADR-0015); *signal degradation* (a field, never silent).

### Q4 — Edit integrity

- **Source / Stimulus:** a request for the word-level diff of two revisions, or a
  revert of one accepted edit.
- **Artifact:** document revisions (git) + block IDs (SQLite).
- **Environment:** chapter-length document.
- **Response:** word-level diff of the *changed block set* (not whole-doc); revert
  of one accepted edit.
- **Response-measure:** diff ≤ **100 ms** for two revisions of a chapter-length
  doc; a revert changes **only** the blocks that edit touched + a new commit —
  adjacent block IDs untouched.
- **Tactics:** *maintain an index* (stable UUIDs, ADR-0020); *don't repeat
  computing / cache results* (diff over the changed block set, not the whole
  document); *limit the geometry* (commit == one accepted edit).

### Q5 — Testability

- **Source / Stimulus:** a change to any module.
- **Artifact:** each module's public DTO interface.
- **Environment:** CI.
- **Response:** the module is exercised through its public API against stubbed
  dependencies.
- **Response-measure:** **100 %** of public ops have a stub-backed boundary test;
  module internals are unexported (R1), and dependencies are interface types — a
  compile-time guarantee that no module reaches another's internals.
- **Tactics:** *abstract common services* (every module depends on interfaces);
  *test at the boundary* (R5); *sealed by default* (R1).

## Consequences

- **+** Each quality goal is now a testable acceptance criterion with a number and
  an SEI tactic, not prose.
- **+** The measures are honest about hardware (Q3 inherits 60 s, not an
  impossible sub-5 s) and about architecture (Q1's 0 sum-error is *by
  construction*, from ADR-0016's scale-to-total).
- **−** Where a measure was previously absent, the number is now normative and
  must be enforced in CI (Q1's ≤100 ms, Q4's ≤100 ms, Q5's 100 % boundary coverage).

## Alternatives considered

- **Attribution error as a per-component % budget** — rejected: the Meter scales
  onto exact totals, so sum-error is zero by construction; a % budget measures the
  wrong thing.
- **Tighter fallback bound (<5 s)** — rejected: a cold model `start` is bounded at
  60 s by ADR-0007; a sub-5 s target either fakes readiness or forces an
  always-warm fleet.
- **Integration-only testing for Q5** — rejected: cannot prove per-module
  isolation, which is the whole point of the base model.
