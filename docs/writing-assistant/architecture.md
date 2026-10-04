# Academic Writing Assistant — Architecture (arc42)

arc42 skeleton, 12 sections. Related: [ADR log](adr/), [behavioral contracts](behaviors/), [precise contracts](contracts/), [traceability](traceability.md).

---

## 1. Introduction & Goals

### 1.1 Requirements overview

A local-first, single-machine assistant for academic writing and editing. The
active client is the terminal TUI (Ratatui, Rust — ADR-0046); the Tauri/web
editor is landed but **frozen** (ADR-0044). The engine is a **context engine**: it indexes the author's markdown
vault under author-controlled, multi-root corpus scope (workspace-scoped,
ADR-0049), retrieves and gates evidence, assembles the model payload, and makes
every token — and every inclusion, exclusion, and drop — visible and replayable.
It is **not** an inference engine (that's delegated) and **not** a full IDE. One
user governs it, and that user also controls — from `macos-dev-config` — **which
models are served on the machine**.

### 1.2 Quality goals (top 6)

Ranked; each has a measurable, Gherkin-style scenario and links to its ADR.

| # | Goal | Scenario (Gherkin-style) | Linked |
|---|---|---|---|
| Q1 | **Transparent token cost** | Given any turn, when the engine assembles the payload, then a per-component breakdown is reported and every lever change is visible in the meter | ADR-0011 |
| Q2 | **Modifiability** | Given a new model or mode, when the manifest/mode data is edited, then behavior changes with no engine rebuild | ADR-0006, ADR-0009 |
| Q3 | **Hot-swappable serving** | Given a preferred model is down, when a turn runs, then a tagged fallback serves it and the substitution is labeled | ADR-0005, ADR-0015 |
| Q4 | **Edit integrity** | Given any accepted edit, when it is committed, then it is versioned in git and revertible at word-level granularity | ADR-0004 |
| Q5 | **Testability** | Given any module, when exercised through its public API, then it can be verified in isolation against a stubbed endpoint | ADR-0001 |
| Q6 | **Explainable context** | Given any turn, when the context is assembled, then a persisted snapshot reports every message's component and provenance, the retrieval/decision outcomes, and every truncation or drop is labeled | ADR-0044, ADR-0011 |

### 1.3 Stakeholders

| Stakeholder | Expectations |
|---|---|
| Primary user (the operator/author) | control served models from macos-dev-config; see token cost; swap frontends without rework |
| Future machine (GPU box) | consume the archived full-precision models; re-run the fleet policy (ADR-0015) |
| Remote machine | reach serving over the control daemon without replicating the local runner setup (ADR-0025) |
| Client authors (TUI/Tauri) | a stable, codegen'd OpenAPI contract to build against |

## 2. Constraints

| Constraint | Source |
|---|---|
| Follows the base model: compact modules, sealed by default, cross-module access only via defined public APIs; deviation requires an ADR | Framework (ADR-0001) |
| Local-first, single machine; privacy preserved (no public hosting requirement) | architecture.md |
| No Node/Python at runtime for the engine; single static Go binary (no CGO) | ADR-0003 |
| Mac mini M4, 32 GB unified memory, 120 GB/s bandwidth | hardware |
| Model serving is external over REST (llama.cpp / MLX-LM / MLX-VLM) | ADR-0005, ADR-0030 |
| Local runners use the Metal GPU backend; no CPU-only or CUDA path | ADR-0030 |
| The model fleet is defined in `macos-dev-config`, not in the engine repo | ADR-0006 |

## 3. Context & Scope

### 3.1 System context (C4 L1)

```mermaid
flowchart LR
    U([User]) --> T[TUI — active]
    U --> M[Markdown editor — frozen, ADR-0044]
    T --> E[Writing Assistant engine]
    M --> E
    E --> S[(Serving: llama.cpp / MLX, Metal)]
    E --> D[(SQLite + git)]
    E --> C[macos-dev-config: fleet manifest + control daemon + serve.sh]
    S --> W[(Model weights on SSD)]
```

### 3.2 External interfaces

| Interface | Protocol | Notes |
|---|---|---|
| Model serving | OpenAI-compatible REST + SSE | Layer 0; reached via the Provider gateway (ADR-0005, ADR-0016) |
| Fleet manifest | file (JSON) + JSON Schema + semantic lanes validator | two-tier, in macos-dev-config, read only by the daemon (ADR-0018) |
| Serving lifecycle | the control daemon's HTTP verb contract | `serve.sh` wrapped by the daemon (ADR-0007, ADR-0025) |
| Model downloads | HF API (`huggingface-cli`) | provisioning (ADR-0008) |
| Clients | REST + SSE (the OpenAPI contract) | codegen: ogen (Go) · Hey API + Zod (TS) · openapi-to-rust (Rust) (ADR-0017); Rust codegen regenerated for the Ratatui TUI (ADR-0046); Tauri/web remain frozen (ADR-0044) |

## 4. Solution Strategy

The architecture's defining moves:

1. **Clients are dumb; one engine owns everything.** All logic and state in the
   Go engine; clients are generated from a single OpenAPI spec (ADR-0002, 0003,
   0017).
2. **The fleet manifest is the control panel.** What models are servable is
   *data* in macos-dev-config, read **only** by the control daemon, which the
   engine's Fleet gateway talks to over HTTP (ADR-0018, 0025).
3. **Serving control is a verb contract, transported by a daemon.** Lifecycle
   (`list/start/stop/status/log/reach/provision`) is a defined, idempotent
   contract (ADR-0007), served over HTTP by a daemon wrapping `serve.sh`
   (ADR-0025).
4. **One metered choke point.** The context assembler produces the payload *and*
   its per-component token attribution; the Meter scales that onto
   provider-reported totals (ADR-0011, 0016).
5. **Storage split by concern.** Per-service SQLite files for metadata/search/
   embeddings/history; git for versioning; the `Retriever` behind an interface
   (ADR-0004, 0020).
6. **Context is the product.** The engine's value is what reaches the model:
   indexed vault → retrieval → decision gate → one metered assembler → provider,
   with a persisted, explainable snapshot per turn (ADR-0044, ADR-0011). The
   author controls what is retrievable (multi-root, workspace-scoped corpus
   scope) and what enters a turn (the context tray), both engine-side
   (ADR-0049). The TUI is the active client; Tauri/web are frozen.

## 5. Building Block View

### 5.1 Container view (C4 L2)

```mermaid
flowchart TB
    subgraph clients[Clients]
        TUI[TUI — Ratatui (Rust), active — ADR-0046]
        Tauri[Tauri editor — frozen, ADR-0044]
    end
    Engine[Go engine — single daemon]:::engine
    subgraph serving[Serving — macos-dev-config]
        Manifest[(fleet manifest)]
        Daemon[Control daemon]
        Exec[serve.sh executor]
        Runners[llama.cpp / MLX (Metal)]
    end
    subgraph store[Storage]
        SQLite[(SQLite: per-service files)]
        Git[(git repo)]
    end
    TUI -->|REST+SSE| Engine
    Tauri -->|REST+SSE| Engine
    Engine -->|OpenAI REST| Runners
    Engine -->|lifecycle verbs| Daemon
    Daemon -->|read| Manifest
    Daemon -->|wrap| Exec
    Exec --> Runners
    Engine --> SQLite
    Engine --> Git
    classDef engine fill:#e8f0fe,stroke:#4a7
```

### 5.2 Component view (C4 L3)

```mermaid
flowchart TB
    subgraph Engine[Go engine]
        API[API server]
        Loop[Agent loop]
        Assembler[Context assembler]
        Mode[Mode registry]
        ToolReg[Tool registry]
        ToolExec[Tool executor]
        Decider[Tool decider]
        Prov[Provider gateway]
        Fleet[Fleet gateway]
        Meter[Token metering]
        Ret[Retriever]
        Chunker[Chunker]
        TextFormatter[TextFormatter]
        Sess[Session store]
        Doc[Document store]
        FS[Filesystem]
        WStore[Workspace store]
        Shards[Shard manager]
        Corpus[Corpus service]
        Bus[SSE event bus]
    end
    Daemon[Control daemon]
    Manifest[(fleet manifest)]
    Exec[serve.sh]

    API --> Loop
    API --> Doc
    API --> Mode
    API --> Fleet
    API --> FS
    API --> WStore
    API --> Shards
    API --> Corpus
    Corpus --> WStore
    Corpus --> FS
    Corpus --> Shards
    Loop --> Mode
    Loop --> ToolReg
    Loop --> ToolExec
    Loop --> Assembler
    Loop --> Prov
    Loop --> Fleet
    Loop --> Doc
    Loop --> WStore
    Loop --> Shards
    Loop --> FS
    Loop --> Decider
    Decider --> Fleet
    Decider --> Prov
    Assembler --> Mode
    Assembler --> ToolReg
    Ret --> Fleet
    Ret --> Prov
    Ret --> Chunker
    Ret --> FS
    Doc --> TextFormatter
    Shards --> Ret
    Shards --> Sess
    Shards --> Meter
    Fleet --> Daemon
    Daemon --> Manifest
    Daemon --> Exec
    Meter -.meter.-> Bus
    Loop -.events.-> Bus
    Bus -.-> API
```

| Component | Responsibility | Public API | Hidden internals |
|---|---|---|---|
| Fleet gateway | model discovery, resolution (merge + gates + fallback), lifecycle | `ListModels`, `Resolve(name, opts) → Resolution`, `Status`, `Start` (blocking), `Stop`, `Provision` (async), `Fingerprint` (router sync gate) | daemon HTTP client, fallback ladder |
| Provider gateway | OpenAI-compatible REST/SSE calls | `Chat(ctx, target, params)`, `Stream(ctx, target, params, emit)`, `Embed(ctx, target, text)` | retry/backoff, `-np 1` serialization |
| Agent loop | turn loop (thin orchestrator, session-scoped) | `Run(ctx, task) → (turnID, err)` (async) | turn state machine, dispatch/observe, snapshot build/persist, `RouteTurn`, auto-RAG `rag` + `context` events |
| Mode registry | prompt presets as data (name + system prompt + default model) | `List`, `Get` | validation, file loading |
| Pipeline policy | one global turn policy: step cap, context budgets, auto-RAG top-k (ADR-0045) | `Policy` | schema validation, `config/pipeline.json` |
| Tool registry | tool definitions + schemas (all tools global) | `Register`, `List` | schema validation |
| Tool executor | tool execution (ctx carries the turn's shard services) | `Invoke(ctx, name, args)` | name-keyed handler map |
| Tool decider (optional) | tool-intent resolution ("which tool, what args") from a writer's `request_tool` intent — **parked/unwired** (ADR-0045) | `SignalTool`, `Decide(ctx, intent, c)` | prompt layout, Provider.Chat, τ threshold, `.cact` fingerprint |
| Context assembler | payload + attribution + per-message provenance/drops/budget (pure) | `Assemble(ctx, in) → (Payload, Breakdown)` | layout, truncation, accounting, provenance, labeled drops, budget utilization |
| Token metering | counts + attribution + persistence + per-session aggregation | `Attribute(ctx, turnID, breakdown, counts)`, `SessionUsage`, `SessionBreakdown` | scale-to-total, shard `meter.db`, per-component cumulative aggregate |
| Retriever | hybrid retrieval + provenance + eviction + status, per workspace shard | `Query`, `Index`, `IndexPath`, `Evict`, `Status`, `Get` | embedding, vec0 KNN + FTS5 bm25 fused with RRF, shard `index.db` |
| Chunker | chunking (pure) | `Chunk(tree []Block, maxTokens int)` | splitting algorithm |
| TextFormatter | formatting (pure) | `Normalize(kind, text)`, `Validate(kind, text)`, `Format(kind, text)` | hardcoded opinionated style |
| Document store | document + versions | `Open`, `Save`, `Blocks`, `ApplyEdit`, `Commit`, `Diff`, `History`, `Candidates` | git, block UUIDs, candidate side-table |
| Filesystem (leaf) | read-only filesystem reach: directory listing + bounded raw reads, bounded by `ALLOWED_ROOTS` | `List(ctx, dir) → []Entry`, `Read(ctx, path, maxBytes)`, `AllowedRoots()` | `os.ReadDir`/`os.ReadFile`, canonicalization, typed allowlist refusal, byte caps |
| Workspace store (leaf) | global workspace registry + corpus scope + jobs + routing | `ResolveOrCreate`, `Get`, `List`, `FindContaining`, `Scope`, `SetScope`, job/tombstone/routing methods | `workspaces.db` |
| Shard manager | per-workspace context-state lifecycle (lazy open, migrate, LRU close, leases) | `Services(ctx, workspaceID) → *Lease` | `workspaces/<id>/{index,sessions,meter}.db`, LRU cap, refcounts |
| Corpus service | index-only multi-root corpus: scope, reconcile, status, async jobs, lifecycle hook | `Get`, `SetScope`, `Index`, `Evict`, `NotifyChanged` | glob walk via Filesystem, single-flight jobs, tombstones/errors |
| Session store | sessions + their messages + persisted per-turn snapshots (one per selection/doc), workspace-scoped by shard | `ListByDocument`, `ListByWorkspace`, `Create`, `Resume`, `Append`, `History`, `SaveContext`, `TurnContext` | shard `sessions.db`, `turn_context` retention |
| API server | REST/SSE surface (codegen'd) | routes per OpenAPI spec | framing, turnID↔session↔client correlation |
| SSE event bus | typed event fan-out | `Emit`, `Subscribe` | connection registry, backpressure |

The full module list, precise signatures, and the acyclic dependency graph are
specified in `contracts/module-boundaries.md`. Internal boundaries are **sealed Go
interfaces over pure DTOs** (the locked-service tenet, ADR-0016) — no module
reaches another's types, tables, state, or files, and REST/HTTP exists only at
process boundaries.

### 5.3 Code level (C4 L4)

Deferred — generated from source as it lands. Pre-defined seams/interfaces:
`FleetGateway`, `ProviderGateway`, `Retriever`, `ContextAssembler`,
`Chunker`, `TokenMeter`, `DocumentStore`, `SessionStore`, `EventBus`,
`Filesystem`, `WorkspaceStore`, `ShardManager`, `Corpus`, `ToolDecider` (optional,
ADR-0028), `TextFormatter` (ADR-0029) (see `contracts/interface.md`).

## 6. Runtime View

### 6.1 Happy path

```mermaid
sequenceDiagram
    participant U as User
    participant T as TUI
    participant API as API server
    participant L as Agent loop
    participant A as Context assembler
    participant R as Retriever
    participant P as Provider gateway
    participant F as Fleet gateway
    participant M as Model server
    participant Doc as Document store

    U->>T: "edit this paragraph (proofreader)"
    T->>API: POST /turn (modeName, documentID, blockID, text) → turnID
    API->>L: Run(task)
    L->>Mode: Get(mode)
    L->>F: Resolve(mode.defaultModel, {modeTag})
    F->>M: Status (via daemon)
    F-->>L: Resolution {model, params, degraded, usedName}
    L->>R: Query(text, topK)
    R-->>L: ranked chunks
    L->>A: Assemble(mode, tools, chunks, history, input)
    A-->>L: payload + breakdown
    L->>P: Stream(target, params, emit)
    P->>F: (target resolved earlier)
    P->>M: /v1/chat/completions (SSE)
    M-->>P: token stream + eval counts
    P-->>L: raw token/done/error
    L->>Meter: Attribute(turnID, breakdown, counts)
    Meter-->>L: attributed breakdown
    L->>Doc: ApplyEdit + Commit (on accept)
    L-->>API: token/candidate/diff/done (turnID-tagged)
    API-->>T: token/candidate/diff/done events
    T-->>U: rendered diff + token meter
```

### 6.2 Failure / degradation path

```mermaid
flowchart TD
    A[preferred model down] --> B{Fleet Resolve}
    B -->|tagged fallback up| C[select fallback]
    C --> D[label degraded=true, usedModel]
    B -->|no fallback| E[emit error no-model-available]
    F[model not provisioned] --> G[emit provision-required]
    G --> H[offer provision verb]
```

## 7. Deployment View

```mermaid
flowchart TB
    subgraph mac[Mac mini M4 / 32GB]
        Engine[Go engine: daemon or Tauri sidecar]
        subgraph desktop[Desktop — frozen, ADR-0044]
            Tauri[Tauri app + sidecar engine]
        end
        subgraph web[Web — frozen, ADR-0044]
            UI[Vue+CodeMirror served]
        end
        TUI[TUI]
        subgraph serving[Serving]
            Manifest[(fleet manifest)]
            Exec[serve.sh]
            Runners[llama.cpp / MLX (Metal) on local ports]
        end
        SSD[(Ex-SSD: models/ + caches/)]
    end
    Runners --> SSD
    Engine -->|REST| Runners
```

- **TUI: the active client** (ADR-0044) — standalone Ratatui (Rust) terminal
  client, same contract (ADR-0046); the OpenTUI client is frozen and retired on
  parity.
- Desktop (Tauri): Go engine bundled as a sidecar, spawned by the Rust core.
  **Frozen** — landed, no new work (ADR-0044).
- Web: same UI served locally; engine self-hosted on the machine/LAN.
  **Frozen** with Tauri (ADR-0044).

## 8. Cross-cutting Concepts

| Concept | Approach |
|---|---|
| Module sealing / public APIs | sealed Go interfaces over **pure DTOs** (locked-service tenet); no REST between modules — `contracts/module-boundaries.md` (ADR-0001, ADR-0016) |
| Fleet manifest (control panel) | two-tier JSON (daemons + models) in macos-dev-config, JSON-Schema + semantic lanes validator — `contracts/data-model.md` §2 (ADR-0018) |
| Serving lifecycle | idempotent verb contract, transported by an HTTP **control daemon** wrapping `serve.sh` — `contracts/interface.md` §12 (ADR-0007, ADR-0025) |
| Provisioning | HF API download + lanes rule; async + observable — ADR-0008, ADR-0018 |
| Token metering | assembler accounting scaled onto provider totals by the Meter; thinking approximated when omitted — ADR-0011, ADR-0016, ADR-0024 |
| Streaming | SSE typed events + NDJSON fallback; events turnID-correlated — ADR-0012, ADR-0017 |
| Versioning | git (coarse, commit-per-AI-edit + autosave) + stable UUID block IDs (fine) + candidate side-table — ADR-0004, ADR-0020 |
| Fleet policy | MoE over dense, 14B+ citation floor, temperature sheet — ADR-0015 |
| Tool routing | optional `ToolDecider`: parked — the loop no longer reads `toolCalling`; seam and startup gates remain in-tree, unwired — ADR-0028, ADR-0045 |
| Prompt presets | modes are `name` + `systemPrompt` + `defaultModel`; one fixed pipeline (all tools, one global step cap and budget set); tabs are presentation, the wire term stays `mode` — ADR-0045 |
| Write-through safety | approve = commit + atomic mirror; open and pre-write hash checks; typed `file-changed-externally` conflict; newest-first, base-validated candidates; guards live on the model path — ADR-0047 (amends ADR-0039) |
| Vault locate | `/locate` command; deterministic markdown-stripped exact-then-fuzzy search over the open document then the vault index; typed `LocateResult`; anchored, guarded edit — ADR-0048 |
| Decision layer | retrieval gating as a second metered model call; one global pipeline policy; fail-open with labeled degradation — ADR-0044 (extends ADR-0028); no per-mode config (ADR-0045) |
| Context inspector | persisted per-turn snapshot: assembled messages with component + provenance, retrieval/decision outcomes, budgets, labeled drops; engine data, clients render — ADR-0044, ADR-0011 |
| Context management | workspace registry + multi-root corpus scope (roots + include/exclude, per-document status, idempotent eviction) and the per-turn context tray (pin/remove, retrieval query, auto-RAG); Workspace ≠ Corpus — the workspace root bounds browsing/editing only — ADR-0049 |
| Workspace-scoped storage | global `app.db` + git/worktree for document identity, `workspaces.db` for the registry; per-workspace context-state shards (`index.db`/`sessions.db`/`meter.db`) opened lazily — one file per service *instance*, not per service — ADR-0049 (amends ADR-0016) |
| Filesystem boundary | `ALLOWED_ROOTS` bounds `GET /directories` browsing and corpus indexing; outside paths are typed refusals, even at `ENGINE_BIND=0.0.0.0` — ADR-0049, ADR-0021 |
| Edit formatting | the engine owns the bytes: whole-block edits, `TextFormatter` normalize/validate/format, block-level guard, structured edit result — ADR-0029 |
| Workspace navigation | engine-served shallow directory listing (Filesystem leaf, renamed by ADR-0049 §5) + turn-scoped, metered `@`-mentions that are read-only context, never versioned documents — ADR-0035, ADR-0036 |
| Document reader | read-only rendered markdown over the engine block tree (`GET /documents/{id}/blocks`, joined fragments, `tui-markdown`); a toggleable pane shaped over a `Block[]` view-model to extend into an editor, with the `SaveTree` write path left unwired; approve stays the only write boundary — ADR-0050 |
| Inference control surface | a future `InferenceControl` interface *behind* the Provider seam (a sibling of `ProviderGateway`, not a change to it); the "knobs" (logprobs, grammar, KV, speculative decoding) are decoupled from the OpenAI-compatible contract for the MVP — `research/vision-native-local-llm-text-editing.md` |
| Deployment/security | sidecar spawn dynamic-port-default; localhost bind; Tailscale deny-by-default — ADR-0021 |

## 9. Architectural Decisions

Full records in [adr/](adr/). Index:

| # | Decision | Status |
|---|---|---|
| 0001 | Base model: compact modules + explicit public APIs | Accepted |
| 0002 | Layered, REST-first process architecture; dumb clients | Accepted |
| 0003 | Single Go engine binary + contract-first codegen | Partially superseded by 0017 (codegen tool selection) |
| 0004 | SQLite (meta/vec/FTS) + git versioning; Retriever interface | Accepted |
| 0005 | Provider gateway: name → endpoint + capabilities | Superseded by 0016 |
| 0006 | Fleet manifest (JSON) in macos-dev-config as source of truth | Superseded by 0018 |
| 0007 | Serving lifecycle verbs as a defined control contract | Partially superseded by 0025 (the "no HTTP daemon" conclusion); verb contract unchanged |
| 0008 | Model provisioning via HF API | Partially superseded by 0030 (source kinds narrowed) |
| 0009 | Mode registry: modes as data | Superseded by 0019 |
| 0010 | Tool registry: tools as data with JSON schemas | Superseded by 0016/0019 |
| 0011 | Context assembler: single metered choke point | Superseded by 0016 (scale-to-total), 0022 (measurable target), 0024 (thinking tokenizer) |
| 0012 | SSE typed events + NDJSON fallback | Accepted |
| 0013 | Clients: OpenTUI first, Tauri later, both dumb | Partially superseded by 0023, then 0046 (TUI technology); Tauri half frozen (0044) |
| 0014 | Deployment targets + capability adapter | Partially superseded by 0021 (sidecar spawn mechanics only); Tauri/web targets frozen (0044) |
| 0015 | Fleet sizing policy (MoE, 14B+ floor, temperature) | Accepted |
| 0016 | Module inventory + exact public APIs, pure-DTO boundaries | Accepted |
| 0017 | OpenAPI contract surface: endpoints, SSE, codegen (ogen/Zod/openapi-to-rust) | Accepted — Rust codegen regenerated for the Ratatui TUI (0046); Tauri/web remain frozen (0044) |
| 0018 | Fleet manifest: two-tier + serve.sh migration + lanes + async provision | Partially superseded by 0030 (runner enum narrowed) |
| 0019 | Modes/tools as data: engine-repo, fail-fast, name-keyed handler bind | Amended by 0045 (behavioral mode fields removed; one turn pipeline) |
| 0020 | Storage: commit cadence, worktree, UUID blocks, candidates, Chunker | Accepted |
| 0021 | Deployment + security: sidecar spawn, bind policy, Tailscale-only | Accepted — sidecar/Tauri-facing parts frozen (0044) |
| 0022 | Quality goals as measurable SEI scenarios | Accepted |
| 0023 | OpenTUI renderer: Solid | Superseded by 0046 (Ratatui TUI v2) |
| 0024 | Thinking-token attribution: bundled tokenizer fallback | Accepted |
| 0025 | Serving control transport: HTTP control daemon wrapping serve.sh | Accepted |
| 0026 | Sessions as first-class entities (session store, per-session concurrency, budget) | Accepted |
| 0027 | Locked-service tenet: shared-DTO ownership + stream seams; daemon sole manifest reader | Accepted |
| 0028 | Tool decider: optional router ("writer signals, specialist decides") | Accepted — parked by 0045 (no mode enables the router; seam unwired) |
| 0029 | Edit verification + TextFormatter: "the engine owns the bytes" | Accepted |
| 0030 | Fleet substrate: pure llama.cpp + MLX on Metal | Accepted |
| 0031 | SSE server transport is hand-framed; ogen scope clarified | Accepted |
| 0032 | Control daemon authored in texteditor; lifecycle decision-gaps closed | Accepted |
| 0033 | Control daemon source moved to macos-dev-config (the machine-local LLM control plane) | Accepted |
| 0034 | Repository layout: client/server split, contract at root | Accepted |
| 0035 | Directory listing: engine-served Workspace capability | Accepted |
| 0036 | File mentions: metered, turn-scoped context attachments | Accepted |
| 0037 | API server CORS policy: explicit origin allowlist for the webview/web targets | Accepted — web/Tauri frozen (0044) |
| 0038 | Manual-edit wire route: `PUT /documents/{id}/tree` autosave path | Accepted |
| 0039 | Manual saves and accepted edits write through to the opened file | Amended by 0047 (auto write-through + conflict detection); Tauri-facing flow frozen (0044) |
| 0040 | Fleet observability surface: `/fleet`, batch status, last-good cache | Accepted |
| 0041 | Floating draggable chat window (Tauri) | Accepted — frozen (0044) |
| 0042 | Tailwind v4 + shadcn-vue for the Tauri client | Accepted — frozen (0044) |
| 0043 | One-script Tauri build (`tools/build-tauri.sh`) | Accepted — frozen (0044) |
| 0044 | Context-engine north-star: TUI-first, explainable context, decision layer | Accepted — extended by 0045 (no per-mode decision config) and 0046 (Rust TUI) |
| 0045 | Prompt presets: one turn pipeline, behavioral mode fields removed | Accepted |
| 0046 | TUI v2: standalone Ratatui (Rust) client, replacing OpenTUI | Accepted — amended by 0050 (reader pane) |
| 0047 | Auto write-through on approve with external-change detection | Accepted |
| 0048 | `/locate`: anchor a pasted chunk to its vault location | Accepted |
| 0049 | Context management: workspaces, multi-root corpus, allowed-roots boundary, context tray | Accepted — amends 0016 (per-instance SQLite), 0026 (workspace-scoped sessions), 0035 §3 (workspace entity; leaf rename) |
| 0050 | TUI reader pane: read-only rendered markdown over the engine block tree, editor-extensible | Accepted — amends 0046 §5/§9 |

## 10. Quality Requirements

### 10.1 Quality tree

```mermaid
flowchart TD
    Q[Quality] --> Transparency[Transparent token cost]
    Q --> Modifiability
    Q --> Swappability[Hot-swappable serving]
    Q --> Integrity[Edit integrity]
    Q --> Testability
    Q --> Explainability[Explainable context]
```

### 10.2 Quality scenarios

Each is an SEI general scenario with a concrete response-measure (ADR-0022).

| # | Scenario | Response-measure |
|---|---|---|
| Q1 | Given any turn, the per-component token breakdown is reported and lever changes are visible (ADR-0011/0016) | breakdown ≤100 ms after usage lands; scaled sum equals provider total exactly; overflow labeled |
| Q2 | Given a model/mode edit in data, behavior changes with no rebuild (ADR-0018/0019) | next-turn effect, 0 rebuilds; startup validate ≤50 ms |
| Q3 | Given a down model, a tagged fallback serves and is labeled (ADR-0015/0016) | fallback ≤60 s cold; degradation label guaranteed |
| Q4 | Given an accepted edit, it is git-versioned and word-level revertible (ADR-0020) | diff ≤100 ms; revert isolates blocks |
| Q5 | Given any module, it is verifiable in isolation through its public API (ADR-0001/0016) | 100% of public ops stub-tested |
| Q6 | Given any turn, the assembled context is persisted and explainable — provenance per message, decision outcomes, labeled drops (ADR-0044/0011) | snapshot retrievable after the turn; 0 unlabeled drops; snapshot ↔ payload agreement |

### 10.2b Functional behavior contracts

| Feature file | Concern | Source ADRs |
|---|---|---|
| serving-control.feature | manifest + verbs + provisioning | 0006, 0007, 0008, 0018, 0025 |
| provider-hotswap.feature | fallback + citation floor | 0005, 0009, 0015, 0016, 0019 |
| token-metering.feature | per-component attribution | 0011, 0016, 0022, 0024 |
| versioning.feature | git + block IDs | 0004, 0020 |
| client-swap.feature | dumb generated clients | 0002, 0013, 0016, 0017, 0023 |
| sessions.feature | persisted sessions, per-session concurrency + budget | 0026 |
| tool-routing.feature | writer-signals-router-decides, per-mode toggle, fail-fast gates | 0028 |
| edit-integrity.feature | whole-block edits, engine-owned formatting, block-level guard, structured result | 0029 |
| workspace.feature | engine-served directory listing, `@`-mentions as metered read-only context | 0035, 0036 |
| context-inspector.feature | persisted turn snapshots: provenance, retrieval/decision outcomes, labeled drops | 0044, 0011, 0024, 0036 |
| fleet-observability.feature | fleet state surface + last-good cache + remediation | 0040 |
| chat-window.feature | Tauri floating chat window (frozen) | 0041, 0042 |
| locate-anchor.feature | `/locate` chunk anchoring: deterministic resolve, ambiguity, anchored guarded edit | 0048, 0036, 0029, 0047 |
| context-management.feature | workspaces, multi-root corpus scope, allowed-roots boundary, context tray, pin-vs-gate | 0049, 0011, 0036, 0044, 0048 |

### 10.3 Definition of done (documentation)

The documentation set is complete when:

- No `TBD` / placeholder text remains anywhere.
- Every ADR appears in the §9 index (0001 = base model: affirmed or deviated).
- Every module has a documented public API in `contracts/module-boundaries.md` (base model).
- Every §10.2 quality scenario links a Gherkin behavior contract (see traceability.md).
- Cross-cutting mechanisms have a precise contract in `contracts/` and are linked from §8.
- ADR decisions are traceable to arc42 sections, behaviors, and contracts (traceability.md).

## 11. Risks & Technical Debt

| # | Risk | Severity | Mitigation |
|---|---|---|---|
| 1 | Control-daemon boundary is a process-boundary coupling (was `serve.sh` shell-out) | Low | resolved — ADR-0025 wraps `serve.sh` behind the daemon's HTTP contract; the verb contract is the invariant |
| 2 | Attribution of tokens across components is approximate (provider totals) | Medium | assembler accounting scaled onto exact totals; thinking/labeled overline when omitted (ADR-0016, ADR-0024) |
| 3 | `modernc.org/sqlite` slower than CGO for heavy writes | Low | single-user local workload; revisit if write volume grows |
| 4 | Fleet policy is hardware-specific | Medium | re-run ADR-0015 on hardware change; archive preserves models |
| 5 | No auth on local inference servers exposed to LAN | Low | Tailscale ACL deny-by-default + pre-bind gate (ADR-0021) |
| 6 | Bundled per-family tokenizer (thinking fallback) adds binary size + maintenance | Low | scoped to reasoning-prefix counting, omitting-providers only (ADR-0024) |
| 7 | The `.cact` router artifact can drift from the tool vocabulary | Medium | `router-tools-stale` startup sync gate (ADR-0028); re-`needle finetune` or switch the mode back to `native` |
| 8 | Hardcoded formatter style may diverge from model expectations | Low | the style is fixed code, so the model trains against a stable target (ADR-0029); `Validate` catches structural drift |
| 9 | Inference control surface (knobs) deferred | Low | decoupling model: `InferenceControl` is a future *sibling* interface behind the Provider seam, not a Provider change (vision doc); the non-native knobs (logprobs, grammar, logit-bias) arrive via a richer protocol, the native knobs (KV branching, speculative decoding) stay deferred pending in-process; do not bake OpenAI-only assumptions into the loop or assembler |

## 12. Glossary

| Term | Definition |
|---|---|
| Fleet manifest | `models.json` in macos-dev-config; two-tier (daemons + models) declaration of servable models |
| Fleet gateway | engine module that discovers/resolves models and drives lifecycle via the daemon |
| Control daemon | HTTP transport over the lifecycle verb contract; the sole `models.json` reader (wraps `serve.sh`) |
| Lifecycle verbs | the `list/start/stop/status/log/reach/provision` control contract |
| Lane | a model's single assigned runner/daemon (one daemon per model's `source`) |
| Mode | a declarative persona: prompt + default model + tool set + budget |
| Choke point | the context assembler — the single place where the token payload is built |
| Block ID | stable UUID identifier for a paragraph/heading/table, enabling fine-grained versioning |
| Candidate | an unaccepted AI edit, keyed by block ID in a Document-store side-table |
| Session | a persisted conversation — doc-level or anchored to a block selection; multiple per file, runnable concurrently |
| Pure DTO | a boundary type with no behavior; the only thing that crosses a module seam (locked-service tenet) |
| Provision | fetch model weights via the HF API (async, observable) |
| Vault | the author's markdown corpus (thesis chapters + notes) that is indexed and retrieved over |
| Workspace | persistent engine entity: a root directory plus its subdirectories; governs browsing/editing only — Workspace ≠ Corpus (ADR-0049) |
| Workspace store | the sealed `workspaces.db` owner: workspace records, corpus roots/scope, index jobs, eviction tombstones, turn/session routing (ADR-0049 §5) |
| Filesystem | the stateless read-only filesystem leaf (formerly the Workspace leaf), canonicalizing and bounded by `ALLOWED_ROOTS` (ADR-0035, ADR-0049 §5/§6) |
| Workspace shard | per-workspace SQLite context state (`index.db`/`sessions.db`/`meter.db`) under `<data>/workspaces/<id>/`; document identity and git stay global (ADR-0049) |
| Corpus root | an absolute file or directory in a workspace's multi-root corpus; roots may lie outside the workspace root (ADR-0049) |
| Corpus scope | a corpus's roots plus include/exclude globs; engine-owned and index-only — never writes document files (ADR-0049) |
| Context tray | the per-turn view of assembled context components with pin/remove, editable retrieval query, and auto-RAG toggle; the final set is what the snapshot records (ADR-0049) |
| Allowed roots | the `ALLOWED_ROOTS` allowlist bounding directory browsing and corpus indexing; outside paths are refused with a typed error (ADR-0049, ADR-0021) |
| Context snapshot | the persisted per-turn record of the assembled payload: messages, component + provenance, retrieval/decision outcomes, budgets, labeled drops |
| Context inspector | the contract surface + TUI panel that renders context snapshots (ADR-0044) |
| Decision layer | the optional typed-decision model (Laya) that gates retrieval as a second metered call under one global policy (ADR-0044, ADR-0045) |
| Preset | a mode's minimal form — name + system prompt + default model; presented as TUI tabs (ADR-0045) |
| Locate anchor | the document + block resolved from a pasted chunk by `/locate` (ADR-0048) |
| Write-through conflict | `file-changed-externally`: the disk file changed since the engine last read it; the write is refused, never clobbered (ADR-0047) |

---

## Appendix A: Standards mapping (ISO/IEC/IEEE 42010 + SEI Views & Beyond)

| 42010 concept | Where |
|---|---|
| Concerns | §1.2 quality goals, §10.1 tree |
| Viewpoints | Appendix B (declared below) |
| Views | §5 (structure), §6 (runtime), §7 (deployment) |
| Architecture decisions | §9 + ADR log |
| Rationale | ADR Context/Consequences sections |
| Correspondences | Appendix B mapping table |

| SEI viewtype | Style | This system |
|---|---|---|
| **Module** | layered, decomposition | §5.2 components; packages; hierarchy |
| **Component-and-connector** | communicating-processes, shared-data | §6: runtime elements + connectors |
| **Allocation** | deployment | §7: environment/hardware mapping |

## Appendix B: Viewpoint declaration

| Viewpoint | Audience | Concern | Section |
|---|---|---|---|
| Context | everyone | scope, interfaces | §3 |
| Container | architects | technology choices, boundaries | §5.1 |
| Component | developers | responsibilities, seams | §5.2 |
| Runtime | developers/ops | behavior, failure, degradation | §6 |
| Deployment | platform engineers | environment, constraints | §7 |
| Decision | all future maintainers | why | §9, ADR log |
| Behavior contracts | testers/agents | executable rules | behaviors/ |
