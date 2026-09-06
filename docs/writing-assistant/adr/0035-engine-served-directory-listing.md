# ADR-0035: Directory listing — an engine-served Workspace capability

Status: Accepted

Extends: ADR-0016 (module inventory), ADR-0017 (OpenAPI contract surface).

## Context

The user wants to launch the TUI as `texteditor .` — the directory the TUI is
opened from becomes a *workspace* whose files the TUI knows and offers (e.g.
`@filename` in chat, ADR-0036). Two questions follow:

1. **Where does the file listing come from?** The TUI has no filesystem reach:
   ADR-0014 gives it "no native shell" (its capability adapter is the engine),
   ADR-0013 §3 makes it a dumb client, and ADR-0017 routes every client
   capability through the OpenAPI contract. Today the engine already does the
   TUI's file work (`POST /documents {path}` opens by path). A client-side
   `fs.readdir` would be the first domain capability smuggled into a client —
   it would also be re-implemented per client, breaking the frontend-swap
   guarantee (ADR-0002 §"Clients are dumb").
2. **Which module owns it?** The Document store (ADR-0016 §9) owns *versioned
   content* — open/version/edit. Listing a directory and reading a file
   raw are a different concern: stateless filesystem reach with no versioning
   side effects. Widening the Document store would bleed a second concern into
   a locked service (base model R2, ADR-0001).

This feature is a Track-1.5 insertion: it lands before Track 2 (Plan E/F,
`implementation-sequence-future.md`) because both Track-2 clients inherit it
from the contract.

## Decision

### 1. A new sealed `Workspace` module — read-only filesystem reach (leaf)

```go
type Entry struct {
    Name  string // bare file/dir name
    Path  string // absolute path
    IsDir bool
}

type Workspace interface {
    List(ctx context.Context, dir string) ([]Entry, error)
    Read(ctx context.Context, path string, maxBytes int) ([]byte, error)
}
```

- `List` is **shallow, non-recursive** and sorted by name (case-insensitive).
  Hidden entries (dotfiles, e.g. an Obsidian vault's `.obsidian`) are *returned*;
  filtering for display is client-side presentation, not engine policy.
- `Read` is **bounded** (`maxBytes`, caller-supplied) and returns **raw bytes
  only** — it never registers, versions, or indexes anything. Mentioned-file
  context (ADR-0036) reads through here, and must not create git history.
- The module is a **pure leaf**: `os.ReadDir`/`os.ReadFile` plus path validation
  are its hidden internals. It owns no state, no database, no cache.
- Typed errors: `not-found`, `not-a-directory`, `not-regular` (a file where a
  directory was expected, and vice versa), `too-large` (`Read` over `maxBytes`),
  `read-failed`.

### 2. One new contract endpoint

`GET /directories?path=<absolute dir>` → `{ "path": "…", "entries": Entry[] }`,
operationId `listDirectory`, backing `Workspace.List`. Errors project as
`not-found` / `not-a-directory`. `Workspace.Read` gets **no** REST surface:
mentions reach it through `POST /turn` (ADR-0036), and a bare `/files` read
endpoint is deferred until a client actually needs one.

### 3. Workspaces are client-side presentation state

The TUI accepts a *directory* as its first CLI arg (`texteditor .`), holds the
listing in its store, and renders the picker. The engine stays **stateless
about workspaces** — it takes absolute paths, exactly as `DocumentStore.Open`
already does. No workspace concept enters the engine, the database, or the
session model.

## Consequences

- **+** Listing is a contract capability: TUI gets it today, web and Tauri get
  it via codegen tomorrow (ADR-0017); one capability, not N implementations.
- **+** The Document store stays a versioning service; the new module is
  narrow, sealed, and trivially boundary-testable (Q5).
- **+** Mentioned-file reads are provably side-effect-free (no git, no SQLite)
  by construction — ADR-0036's read-only-context guarantee rests on this.
- **−** The endpoint is a path-enumeration primitive: bound by ADR-0021's
  localhost-by-default bind policy, but a LAN-exposed engine (`ENGINE_BIND=0.0.0.0`)
  becomes a directory browser for anyone who can reach it — recorded, not
  mitigated beyond the existing bind policy.
- **−** One more module to maintain and contract (the base-model cost of R1–R6,
  accepted).

## Alternatives considered

- **TUI reads the directory itself** (`Bun.readdir`) — rejected: first domain
  capability in a dumb client (ADR-0013 §3), contradicts ADR-0014's "TUI has
  no native shell", and must be re-implemented per client.
- **Widen the Document store** with `List`/`Read` — rejected: mixes stateless
  FS reach into a versioning service (concern bleed; base model R2).
- **Recursive listing** — rejected: payload growth on large vaults; shallow +
  client-side navigation keeps the primitive minimal. Revisitable if the
  picker needs breadth.
- **A `/files/{path}` read endpoint** — rejected for now: no client reads raw
  files outside a turn today; mentions carry content through `/turn` (ADR-0036).
