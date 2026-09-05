# Module boundaries contract

The per-system record of every module, its public API, and the dependency graph.
Normative per the base model (`ADR-0001`) — sealed-by-default modules exposing
only defined public APIs — hardened by the **locked-service tenet** (ADR-0016):
internal boundaries are sealed Go interfaces over **pure DTOs**, never REST.
REST/HTTP exists only at process boundaries.

Source ADRs: ADR-0001 (base model), ADR-0016 (module inventory + exact APIs),
ADR-0018 (two-tier fleet manifest), ADR-0019 (mode/tool data), ADR-0020 (storage),
ADR-0025 (control daemon), ADR-0026 (sessions).

## 1. Modules

### Engine (Layer 2, Go)

| Module | Owns (concern) | Public API (defined operations) | Hidden internals |
|---|---|---|---|
| **Fleet gateway** | model discovery, resolution (merge + gates + fallback), lifecycle | `ListModels()`, `Resolve(name, opts) → Resolution`, `Status(name) → LiveState`, `Start(name)` (blocking), `Stop(name)`, `Provision(ctx, name) → provisionID` | daemon HTTP client (ADR-0025), verb mapping, fallback ladder |
| **Provider gateway** (leaf) | OpenAI-compatible REST/SSE calls | `Chat(ctx, target, params)`, `Stream(ctx, target, params, emit)`, `Embed(ctx, target, text)` | retry/backoff, per-server `-np 1` serialization, framing |
| **Agent loop** | the turn loop: task → plan → tools → observe → answer | `Run(ctx, task) → (turnID, err)` (async) | turn state machine, dispatch/observe, truncation |
| **Mode registry** (leaf) | declarative modes (persona/model/tools/budget) | `List()`, `Get(name)` | mode file loading (go:embed), validation |
| **Tool registry** (leaf) | tool definitions + JSON schemas | `Register(tool)`, `List()`, `AllowlistFor(mode)` | schema validation, name-keyed binding metadata |
| **Tool executor** | tool execution | `Invoke(name, args) → result` | private `map[name]→handler func` |
| **Context assembler** (leaf) | the exact token payload + per-component attribution | `Assemble(ctx, in) → (Payload, Breakdown)` | prompt layout, budget truncation, attribution accounting |
| **Token metering** | counts + attribution + persistence | `Attribute(ctx, turnID, breakdown, counts) → Attributed` | scale-to-total, `meter.db` writes, thinking-token reconciliation (ADR-0024) |
| **Retriever** | retrieval (semantic + lexical) | `Query(ctx, text, topK) → []Chunk`, `Index(ctx, documentID)` | embedding (via Fleet+Provider), sqlite-vec KNN, FTS5, rerank |
| **Chunker** (leaf) | chunking (paragraph-aligned, size-bounded) | `Chunk(documentBlockTree, maxTokens) → []Chunk` | splitting algorithm |
| **Document store** | document, blocks, version history | `Open`, `Save`, `Blocks`, `ApplyEdit`, `Commit`, `Diff`, `History`, `Candidates` | git (go-git), block-UUID minting, candidate side-table, word-diff |
| **Session store** (leaf) | sessions + their messages | `ListByDocument`, `Create`, `Resume`, `Append`, `History` | `sessions.db` |
| **API server** | the versioned REST/SSE surface (codegen'd) | HTTP routes + SSE endpoints per the OpenAPI spec | framing, validation, turnID↔client correlation |
| **SSE event bus** | typed event fan-out | `Emit(event)`, `Subscribe(filter) → stream` | connection registry, bounded chans |

### Serving (Layer 0, `macos-dev-config`)

| Module | Owns (concern) | Public API (defined operations) | Hidden internals |
|---|---|---|---|
| **Fleet manifest** (data) | what models *can* be served (two-tier: daemons + models) | manifest schema + `Read() → []Model` (validated by the shared loader) | file location, git-ignored local overrides |
| **Control daemon** | HTTP transport over the verb contract; **sole reader of the manifest** | daemon HTTP contract (`list/start/stop/status/log/reach/provision`) | mapping HTTP → `serve.sh`; verb execution |
| **Lifecycle executor** (`serve.sh`) | running/stopping model servers (runner logic) | verb contract (invoked by the daemon) | per-runner CLI flags, `models.json` parse (via jq), health checks, delegate wrappers |
| **Always-on agents** (`launchd/`) | reboot-persistent serving | install/load a named agent | plist templating, `launchctl` load |
| **Tailscale ACL** | remote inference authorization | deny-by-default ACL matching `tag:inference-client` → `tag:inference-server` ports | tailnet tag assignment |

### Clients (Layer 3)

| Module | Owns (concern) | Public API | Hidden internals |
|---|---|---|---|
| **TUI** (OpenTUI + Solid) | terminal rendering + commands | *none exposed to engine* — consumes the OpenAPI spec (Hey API + Zod) | panel layout, renderable wiring |
| **Tauri editor** (later) | native markdown editing | *none exposed to engine* — consumes the OpenAPI spec (openapi-to-rust) | CodeMirror integration, file I/O, popover UI |

Anything **not** listed in "Public API" is private and unreachable from other
modules. The Fleet gateway's only serving-side dependency is the **control daemon's
HTTP contract** (ADR-0025) — it reads the manifest *only* through the daemon,
never the file directly.

## 2. Dependency graph

```mermaid
flowchart LR
    subgraph clients[Clients]
        TUI[TUI]
        Tauri[Tauri editor]
    end
    subgraph engine[Engine]
        API[API server]
        Loop[Agent loop]
        Assembler[Context assembler]
        Mode[Mode registry]
        ToolReg[Tool registry]
        ToolExec[Tool executor]
        Prov[Provider gateway]
        Fleet[Fleet gateway]
        Meter[Token metering]
        Retriever[Retriever]
        Chunker[Chunker]
        Doc[Document store]
        Sess[Session store]
        Bus[SSE event bus]
    end
    subgraph serving[Serving]
        Daemon[Control daemon]
        Manifest[(fleet manifest)]
        Exec[serve.sh]
    end

    TUI --> API
    Tauri --> API
    API --> Loop
    API --> Doc
    API --> Mode
    API --> Fleet
    API --> Sess
    Loop --> Mode
    Loop --> ToolReg
    Loop --> ToolExec
    Loop --> Assembler
    Loop --> Prov
    Loop --> Fleet
    Loop --> Doc
    Loop --> Retriever
    Loop --> Sess
    Assembler --> Mode
    Assembler --> ToolReg
    Retriever --> Fleet
    Retriever --> Prov
    Retriever --> Chunker
    Prov --> Fleet
    Meter --> Bus
    Fleet --> Daemon
    Daemon --> Manifest
    Daemon --> Exec
    Bus -.emit.-> API
    Meter -.emit.-> Bus
    Loop -.emit.-> Bus
```

- Every edge targets a module's **public API**, never its internals.
- The graph is **acyclic**; direction is inward (clients → engine → serving-data).
- Leaf modules (no out-edges) hold pure/deterministic logic: `Mode registry`,
  `Tool registry`, `Context assembler`, `Chunker`, `Session store`,
  `Provider gateway`, and the `Fleet manifest`.
- The `Retriever` is **not** a leaf (depends on Fleet + Provider for the embed call
  and on the Chunker) — a deliberate consequence of ADR-0016.

## 3. Public API signatures

Precise Go signatures and pure-DTO type definitions live in
`contracts/interface.md`:

- **Fleet + Provider + Retriever + Assembler + Meter + Document store +
  Session store + Event bus** — exact Go interface signatures (ADR-0016, ADR-0026).
- **Serving lifecycle** — the verb contract (ADR-0007), now transported by the
  control daemon (ADR-0025).
- The **fleet manifest schema** (two-tier) — `contracts/data-model.md` §2
  (ADR-0018).

## 4. Invariants

- No cross-module dependency reaches outside a defined public API (R1–R3).
- Every boundary type is a **pure DTO** (no behavior, no pointers into another
  module's state, no embedding of another module's types) — the locked-service
  tenet (ADR-0016).
- The dependency graph is acyclic (R4).
- Each public API is narrow and stable (R2) and contracted (R6).
- The engine depends on serving *only* through the Fleet gateway → control daemon
  HTTP contract (ADR-0025). It never reads `models.json` directly and never
  invokes `serve.sh` directly.
- The Provider, Context assembler, and Mode/Tool registries are all pure leaves.
