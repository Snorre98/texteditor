# ADR-0034: Repository layout — client/server split, contract at root

Status: Accepted

## Context

The repository grew a second deliverable type. It was laid out as a flat Go
module (ADR-0003: one static engine binary) with the contract (`api/`), the
docs-as-code tree (`docs/writing-assistant/`), and the first client (`tui/`)
as siblings. Two facts make that layout stop fitting:

1. **Clients multiply.** The OpenTUI client (ADR-0023) is the *first* client,
   not the only one: the layered model (ADR-0002) already plans a Tauri
   markdown editor as a second, richer client. A future repo where `tui/` and a
   `tauri/` app sit at root next to `go.mod` and `docs/` no longer sorts by
   deliverable.
2. **The root is three homes at once.** `go.mod`/`go.sum` (the engine module),
   `api/openapi.yaml` (the cross-cutting contract, ADR-0017), and the docs-as-code
   tree all claim the repo root. Anything layout-sensitive in code (codegen
   entrypoints, path-walking tests) resolves against an ambiguous "root" that
   mixes the Go module root with the repo root.

## Decision

### 1. `server/` is the Go module

`go.mod`, `go.sum`, `cmd/`, `internal/`, `shared/`, and `config/` move to
`server/`. The module path stays **`texteditor`** — all imports remain
`texteditor/...`, zero source churn. Go commands (`build`, `test`, `generate`)
run from `server/`; the repo root is no longer a Go module.

### 2. `client/` hosts client apps; `client/tui/` is the OpenTUI client

`tui/` moves to `client/tui/`. `client/` is the reserved home for the future
Tauri markdown editor, which will land as a sibling of `tui/` (e.g.
`client/tauri/`).

### 3. `api/` stays at the repo root

The OpenAPI spec is the single source of truth consumed by *every* codegen
(the ogen Go server, the Hey API + Zod TS client, future clients), so it
belongs to neither `server/` nor `client/`. Contract-first stays visible: the
boundary sits above both sides (ADR-0017, ADR-0002).

### 4. All docs-as-code under `docs/`

The two stray root documents fold into the existing tree:

- `architecture.md` → `docs/architecture.md`
- `local-llms-for-writing.md` → `docs/writing-assistant/research/`

### 5. Historical docs keep their old paths

ADRs and handoffs record decisions as made, with the paths that were true at
the time. They are not rewritten to track this move; ADR immutability wins
over path freshness. This ADR is the single record of the transition.

### 6. Layout-sensitive code is fixed, not guessed at

- `server/internal/ogen/main.go`: repo root is resolved three levels up from
  `server/internal/ogen` (the repo root holds `api/`).
- `server/internal/fleet/contract_mirror_test.go` (ADR-0033 §3): `repoRoot`
  now walks up to the directory holding `docs/writing-assistant` — the repo
  root — instead of the `go.mod` location (which is `server/`).
- `client/tui/openapi-ts.config.ts`: input is `../../api/openapi.yaml`.
- `.gitignore`: stale engine binary paths removed; Go artifacts, env files,
  editor dirs, logs, and a global `node_modules/` rule added.

## Consequences

- **+** One directory level per deliverable: `server/` (engine), `client/`
  (clients), `api/` (contract), `docs/` (docs-as-code).
- **+** The future Tauri client has a reserved, obvious home with no further
  layout churn.
- **−** Repo root is not a Go module: every Go command needs `cd server` (or a
  workspace-level tool); CI and dev scripts must know the boundary.
- **−** Two "roots" exist conceptually (repo root vs module root); code that
  must distinguish them (codegen, path-walking tests) has to say which one it
  means, as §6 does.

## Alternatives considered

- **Keep the flat layout** — rejected: the client multiplier (fact 1) makes
  the root a grab bag; the restructure only gets more expensive as the second
  client lands.
- **Move `api/` into `server/`** — rejected: the contract is consumed by every
  client codegen, not just the server; burying it under one consumer of it
  repeats ADR-0033's "authority owned by a consumer" mistake at the layout
  level.
- **Move `api/` into a new shared dir (e.g. `contracts/`)** — rejected: `api/`
  is already the established name and location in every ADR reference;
  renaming adds churn without a consumer problem to solve.
- **Rename the Go module during the move** — rejected: the module path
  `texteditor` is load-bearing in every import and in ADR-0032's install
  mechanics; renaming is pure churn with no layout benefit.
- **Rewrite historical docs to match the new paths** — rejected: ADRs are
  immutable decision records (ADR-0022 §"docs are the truth"); path drift in
  history is accepted and this ADR anchors the transition.
