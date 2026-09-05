# ADR-0027: Locked-service tenet — shared-DTO ownership and stream seams

Status: Accepted

Clarifies: ADR-0016 (locked-service tenet wording), ADR-0002 (REST-first scope).
Supersedes: ADR-0018 §2 (serve.sh's `jq` parse of `models.json`) and §4 ("shared
semantic loader invoked by both the engine and `serve.sh`"), and ADR-0025 §1's
assumption that `serve.sh` retains its own `models.json` parse.

## Context

The locked-service tenet (ADR-0016) states that the only thing that crosses an
internal module boundary is "a plain data type — no behavior, no pointers into
another module's state, no embedding of another module's types." An adversarial
review of the ADR log surfaced four places where the accepted contracts are either
in tension with that wording or leave a cross-cutting detail unspecified:

1. **Shared DTOs are used everywhere but owned by no one.** `Capabilities`,
   `SamplingParams`, `Chunk`, `Message`, `JSONSchema`, `Document`, `Block`,
   `Revision`, `Candidate`, `Mode`, `Model`, `Target`, `Resolution`, and
   `LiveState` cross many module boundaries. The composite DTOs —
   `AssemblerInput` (embeds `Mode`, `[]Chunk`, `[]Message`, `[]JSONSchema`),
   `Mode` (embeds `SamplingParams`, `ContextBudget`), `Target` (embeds
   `Capabilities`), `Resolution`/`Model` (embed `Capabilities` + `SamplingParams`)
   — literally "embed another module's types," which the tenet forbids as written.
2. **The EventBus and Provider stream seams cross behavior/handles, not plain
   data.** `EventBus.Subscribe` returns a `<-chan Event` (a live handle) and takes
   a filter `func`; `Provider.Stream` takes an `emit func(RawEvent)`. Neither is a
   plain data type, yet both are the accepted Go idiom for streaming.
3. **ADR-0002's "REST-first / everything over REST" wording** invites the
   misreading that REST is used *between* engine modules, when the tenet reserves
   REST for process boundaries only.
4. **`models.json` reader ambiguity.** ADR-0018 §4 said a "shared semantic
   validator … invoked by *both* the engine and `serve.sh`" enforces the lanes
   rule. ADR-0025 made the daemon the sole manifest reader and removed the
   engine's direct manifest access — without recording that ADR-0018 §4's
   engine-and-`serve.sh` loader is superseded, and without pinning whether
   `serve.sh` parses the file itself or receives it from the daemon.

## Decision

### 1. "Pure DTO" is redefined; shared DTOs are centralized

The tenet's "no embedding of another module's types" is read as **no embedding of
another module's *live* types** — structs carrying behavior, methods, channels,
pointers, or mutable state. Composition of other **pure DTOs** is permitted and is
in fact the normal case (a composite request/response DTO).

All boundary-crossing DTOs are **shared, owner-free** types: they live in one
neutral package (`shared`/`dto`) owned by no single module, are **pure** (no
methods, no channels, no pointers, no embedded foreign types beyond other pure
DTOs), and are the *only* types that may appear in a public API signature. The
catalog is:

`Capabilities`, `SamplingParams`, `ContextBudget`, `Mode`, `Model`, `Target`,
`Resolution`, `LiveState`, `Chunk`, `Message`, `JSONSchema`, `Document`, `Block`,
`Revision`, `Candidate`, `WordEdit`, `BlockEdit`, `Event`, `RawEvent`, `ToolDef`,
`Session`, `Payload`, `Breakdown`, `ProviderCounts`, `AttributedBreakdown`.

No module may define a boundary type that embeds another module's package types;
any shared type is promoted to the neutral package rather than imported from a
sibling module.

### 2. Stream seams are an explicit carve-out

Two seams cross a live handle or callback by design, and are documented exceptions:

- **`EventBus.Subscribe(filter) → <-chan Event`** — the returned channel is a
  *stream handle*, not shared mutable state; the `Event` payload is a pure DTO
  (`Data json.RawMessage`). The filter is a consumer-side predicate, not a hook
  into another module's state.
- **`Provider.Stream(ctx, target, params, emit func(RawEvent))`** — `emit` is a
  stream *sink* carrying only `RawEvent` DTOs; it grants no access to Provider
  internals.

Both are recorded as named exceptions to "no behavior, no pointers" in
`contracts/module-boundaries.md` §4. The invariant is unchanged for every other
boundary.

### 3. ADR-0002's "REST-first" is scoped to process boundaries

ADR-0002's "layered, REST-first" topology applies **only to process boundaries**
(client↔engine, engine↔serving). Internal engine module boundaries are sealed Go
interfaces over pure DTOs, never REST. This is a clarification, not a reversal:
ADR-0002's four layers *are* processes, so its decision already conforms; the
wording is tightened so "REST-first" cannot be misread as a license to put REST
between engine modules.

### 4. `models.json` has exactly one reader: the control daemon

ADR-0025 wins. The control daemon is the **sole reader** of `models.json`. It
parses and validates the manifest (structural via the committed JSON Schema;
semantic via the lanes loader), then hands the parsed, validated manifest to
`serve.sh` — `serve.sh` does **not** parse `models.json` directly (no `jq` parse
in `serve.sh`). The engine never reads the manifest at all.

This supersedes ADR-0018 §4's "shared semantic validator … invoked by both the
engine and `serve.sh`": the shared loader now lives in (and is invoked only by)
the daemon, which `serve.sh` consumes downstream. It also supersedes ADR-0025 §1's
assumption that `serve.sh` keeps its own `models.json` parse — `serve.sh` now
receives the parsed manifest rather than parsing the file.

## Consequences

- **+** The tenet is now self-consistent: composite DTOs are legal, shared types
  have a single home, and the two streaming seams are called out rather than
  silently violating the letter of the rule.
- **+** One reader for `models.json` closes the ADR-0018/0025 drift and removes
  the `jq`-in-`serve.sh` regression surface ADR-0018 had accepted.
- **−** A neutral shared-DTO package is a new code-layout convention; the Go
  linter/tooling must treat it as owner-free (no module may add methods there).
- **−** `serve.sh` loses its self-contained `jq` parse; it now requires the daemon
  to supply the manifest. Accepted: the daemon is already the transport for every
  verb, so `serve.sh` is always invoked through it.

## Alternatives considered

- **Flatten all composite DTOs (no cross-module composition)** — rejected: would
  force the assembler to take raw scalars and the loop to splice foreign fields by
  hand, recreating the coupling the tenet is meant to prevent.
- **Allow each module to own its DTOs and import siblings'** — rejected: recreates
  the "embedding another module's types" violation and makes DTO ownership
  ambiguous.
- **Keep `serve.sh` parsing `models.json` via `jq`** — rejected: two parsers of one
  file is the drift ADR-0006/0018/0025 all exist to prevent.
- **No carve-out for stream seams (return DTOs only)** — rejected: would force a
  buffered-copy streaming model that defeats backpressure (ADR-0012, the
  concurrency contract) and the Provider's raw-stream passthrough.
