# Laya decision-layer evaluation (Phase F, ADR-0053)

Status: **method defined; live measurement pending the Laya runner.** The
fixture below is a stand-in for the thesis-scale (60–80 page) corpus; the
harness runs once `laya` is up in the fleet.

## Claim

Gating reduces injected tokens without reducing recall@k on judged-relevant
chunks. This is measured, not a CI gate (ADR-0053 §12).

## Fixture

`server/testdata/decision-golden/`:

- `corpus/*.md` — a small academic corpus, chunked on heading boundaries
  (subheader granularity, matching retrieval and gating).
- `queries.jsonl` — golden queries, each with the human-judged relevant chunks
  named as `file.md#Heading`.

The thesis-scale run replaces `corpus/` with the real 60–80 page vault and
`queries.jsonl` with the author's labeled queries; the metrics and harness are
unchanged.

## Metrics

| Metric | Definition |
|---|---|
| recall@k | judged-relevant chunks surviving the gate / judged-relevant chunks retrieved |
| injected tokens | prompt tokens contributed by retrieved passages (assembler meter) |
| gate latency | wall-clock added by the planner + gate calls |
| keep rate | chunks kept / chunks gated |

## Procedure

1. Index the fixture corpus; for each golden query run a turn with the decision
   layer off (baseline) and on (gated), at a fixed `autoRagTopK`.
2. Read the snapshot `decision.gate.chunks` and the meter `decision` component.
3. Sweep `gateThreshold` in `{0.3, 0.4, 0.5, 0.6, 0.7}` and `breadthTopK`
   (`few`/`many`) and record the metric table.
4. Compare checkpoints (`english` vs `multilingual` vs `typed-decisions`) and,
   where available, fp32 vs ONNX int8 exports.
5. Set the shipped defaults from the sweep; record them in `status.md`.

## Provisional defaults (to be confirmed by the live sweep)

- `enabled: false` (ADR-0044 §5; opt-in per session via the tray).
- `gateThreshold: 0.5` — the P(relevant) midpoint, pending calibration.
- `maxCandidates: 24`, `maxHistoryTurns: 4`, `timeoutMs: 3000`.
- `breadthTopK: {none: 0, few: 3, many: 8}`.

These are starting points; the sweep is expected to move `gateThreshold` and the
`few`/`many` depths.
