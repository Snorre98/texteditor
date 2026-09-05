# ADR-0003: Single Go engine binary + contract-first codegen

Status: Accepted

## Context

The engine must be a local daemon, easy to ship in two ways (standalone and as a
Tauri sidecar). The database must avoid CGO to keep the binary trivially
portable on macOS. Three clients (Go server, TS TUI, Rust/Tauri) must agree on
types without a shared package — so types must be *generated* from one contract.

## Decision

1. **Engine = Go, compiled to a single static binary** running as a local daemon.
   SQLite via `modernc.org/sqlite` (pure Go, no CGO) keeps the binary
   dependency-free (ADR-0004).
2. **Contract-first codegen** from the single OpenAPI/JSON Schema spec:
   - Go server — `oapi-codegen` (or `ogen` if SSE codegen is needed).
   - TS TUI — **Hey API** (matches OpenCode); Zod only if runtime validation is
     wanted, otherwise generated types alone suffice. *Locked for the TUI phase.*
   - Rust/Tauri — `progenitor` or `openapi-to-rust` (the latter for SSE).
     *Deferred to the markdown-editor phase.*
   - `tauri-typegen` — deferred; covers Tauri's internal Rust↔JS IPC, not the Go API.

## Consequences

- **+** One spec is the single source of truth; server and both clients cannot
  drift in shape.
- **+** A no-CGO static binary ships as a sidecar or daemon with zero runtime
  deps (no Node, no Python).
- **−** `modernc.org/sqlite` is slower than CGO sqlite3 for heavy writes; the
  local, single-user workload does not need CGO performance.
- **−** Codegen tooling per language is a build-time dependency to maintain.

## Alternatives considered

- **Hand-written types per client** — rejected: three hand-maintained copies of
  the same schema will drift; violates the frontend-swap guarantee.
- **Node/TypeScript backend** — rejected: the doc explicitly wants a single
  static Go binary, no Node at runtime.
- **Shared protobuf + buf** — rejected: OpenAPI is the ecosystem standard for
  REST/SSE here and maps directly onto OpenAI-compatible serving.
