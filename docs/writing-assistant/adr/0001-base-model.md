# ADR-0001: Base architectural model — compact modules + explicit public APIs

Status: Accepted

## Context

The `define-architecture` method mandates a base model: a system is a set of
compact modules, each **sealed by default** (features private), where the only
way another module may use one is through a deliberately defined **public API**
(narrow, stable, contracted). Deviating requires a recorded rationale.

Forces specific to this system:

- It spans **three languages** (Go engine, TypeScript TUI, Rust/Tauri editor) and
  **two repositories** (`texteditor` for the engine+contract, `macos-dev-config`
  for machine-local serving). There is intentionally **no shared code package**
  between them — so the boundary between modules *is* the architecture.
- The frontend-swap guarantee (clients are dumb, all logic in the engine) only
  holds if the engine's internals cannot leak into a client.
- The serving layer is a different process cluster on a different machine-local
  codebase, so its boundary must be a *contract*, not shared types.

## Decision

Affirm the base model (R1–R6 from the framework) as the normative rule for every
module in the system:

- R1 — Sealed by default: nothing cross-module usable unless in a public API.
- R2 — Explicit public API: narrow, stable, deliberately defined.
- R3 — Public-API-only dependencies: never reach into another module's types,
  tables, state, or files.
- R4 — Inward, acyclic dependency graph; leaf modules hold pure/deterministic logic.
- R5 — Test at the boundary: modules are exercised through their public API.
- R6 — Contracted: each public API is a precise contract in `contracts/`.

The serving side (`macos-dev-config`: the fleet manifest + `serve.sh` executor)
is treated as a **separate module cluster** with its own public API — the
lifecycle **verb contract** (ADR-0007) and the **manifest** data contract
(ADR-0006). The engine's *Fleet gateway* is the only module allowed to depend on
them.

## Consequences

- **+** Every seam is a testable, swappable contract; storage (ADR-0004), serving
  (ADR-0005/0006/0007), and clients (ADR-0013) can be replaced without touching
  the rest.
- **+** Cross-language and cross-repo safety by construction — no accidental
  coupling through shared types.
- **−** More upfront boilerplate: every module needs a documented public API and
  a contract; interfaces are written before implementation is allowed to grow.
- **−** Discipline cost: any "quick" cross-module access must be promoted to a
  public-API decision first.

## Alternatives considered

- **Shared code package / monolith** — rejected: contradicts the cross-language
  frontend-swap guarantee; couples engine internals to clients.
- **Loose "public" modules with no enforced sealing** — rejected: without R1/R3
  the boundary rots and the serving-control API (the crux of this system) blurs
  back into shell scripts reaching into the engine's files.
