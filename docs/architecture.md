# Academic Writing Assistant — General Architecture

A local-first, single-machine assistant for academic writing and editing. You drive it from a terminal UI or a Tauri markdown editor; it reasons over your own notes and ingested literature, edits markdown in place, and — deliberately — makes every token that goes into a model call visible, so the system itself becomes the way you learn what costs tokens and what doesn't.

> **Status:** see [`writing-assistant/status.md`](writing-assistant/status.md) for
> what is complete and what is TODO, cross-checked against the ADR set and the code.

It is **not** an inference engine (that's delegated) and **not** a full IDE.

## Stack

- **Engine/backend** — **Go**, a single static binary running as a local daemon.
- **TUI** — [OpenTUI](https://opentui.com), the terminal-UI library from the OpenCode team (Zig core, TypeScript bindings; write TS directly or via React/Solid).
- **Markdown editor** — **Tauri 2** (Rust core + system WebView) with a Vue 3 + CodeMirror 6 frontend, in the spirit of [Texodus](https://github.com/w512/texodus). No Node at runtime; Node/Bun is build-time only.
- **Model serving** — external, over REST, reached through the machine's control daemon (`macos-dev-config`); runners are `llama.cpp | mlx-lm | mlx-vlm | delegate` on the Metal GPU (ADR-0030).
- **Database** — SQLite via `modernc.org/sqlite` (pure Go, no CGO), the single-file app DB.
- **Contract** — a single OpenAPI/JSON Schema spec (see below), the source of truth shared by every client.

## Layers (not flat)

```
┌────────────────────────────────────────────────────────────────┐
│ Layer 3 — Clients (dumb, swappable)                             │
│   OpenTUI TUI (TS)   ·   Markdown editor (Tauri: Rust+Vue+CM6)  │
└──────────────────────────┬─────────────────────────────────────┘
                           │ one versioned REST API + SSE (typed events)
┌──────────────────────────▼─────────────────────────────────────┐
│ API contract — OpenAPI / JSON Schema                            │
│   (source of truth; codegen → Go server, TS client, Rust client)│
└──────────────────────────┬─────────────────────────────────────┘
┌──────────────────────────▼─────────────────────────────────────┐
│ Layer 2 — Engine (Go) — owns ALL logic + state                  │
│   provider gateway · agent loop · mode registry ·               │
│   tool registry · context assembler · retriever (SQLite) ·      │
│   token metering · document store + version history (git)       │
└──────────────────────────┬─────────────────────────────────────┘
                           │ REST (OpenAI-compatible)
┌──────────────────────────▼─────────────────────────────────────┐
│ Layer 0 — Model serving (external, swappable)                   │
│   control daemon → llama.cpp / mlx-lm / mlx-vlm / delegate      │
└────────────────────────────────────────────────────────────────┘
```

## The frontend-swap guarantee

The single rule that makes swapping the TUI out trivial:

> **Clients are dumb. All logic and state live in the Go engine.**

Clients only render, send commands, stream responses, display the token meter, and show diffs. They contain no domain logic — no RAG, no prompt assembly, no mode definitions, no versioning. Because the engine is Go and the clients are TypeScript/Rust, there is **no shared code package**: the **OpenAPI/JSON Schema spec is the contract**, and each side generates its types from it. Adding a new frontend means generating a client from the spec — nothing else.

### Codegen toolchain

- **Go server** — `ogen` (SSE support).
- **TS TUI** — `Hey API` + Zod for runtime validation. **Locked.**
- **Rust/Tauri** — `openapi-to-rust`. **Locked + landed** (F6).
- **tauri-typegen** — *deferred to the markdown-editor phase*; covers Tauri's internal Rust↔JS command IPC (not the Go API) and needs more research.

## Layer 0 — Model serving (treated as external)

The control daemon (`macos-dev-config`) is the sole reader of the fleet manifest
(`models.json`) and serves each model over an OpenAI-compatible REST API through
one of the Metal runners (`llama.cpp | mlx-lm | mlx-vlm`) or a `delegate` wrapper.
The engine never loads weights and never reads the manifest; it reaches serving
only through the daemon's HTTP contract (ADR-0025/0027/0033). This turns the
whole model fleet — a fast 3B for cheap edits, a 26B-A4B / 35B-A3B MoE for
quality, a 24B for prose — into a uniform, hot-swappable resource.

## Layer 2 — Engine (Go; itself modular)

1. **Provider gateway** — a thin abstraction over the REST endpoint(s). Maps a model *name* to a URL, sampling params, and capabilities (context length, thinking mode). Swapping runners changes only this module.
2. **Agent loop / orchestrator** — the turn-taking loop: task → plan → dispatch tool(s) → observe → repeat → answer. Mirrors OpenCode's loop, but the tools are writing tools, not shell/git.
3. **Mode registry** — *declarative* configurations, not code. Each mode is `{ name, system prompt, default model, tool allowlist, params, context budget, toolCalling }`. Shipped modes: `editor`, `drafter`, `proofreader`, `grammar` (a `literature-reviewer` persona is future work). Switching mode is a config change, not a rebuild — the "quickly change model/persona" feature, in the spirit of OpenCode's modes and agents.
4. **Tool registry** — writing-specific tools, each with a JSON schema. Shipped: `diff`, `edit_markdown`, `read_note`, `retrieve` (future: `suggest_revision`, `cite`, `search_vault`). Schemas are data, so the system can show you their token cost.
5. **Context assembler** — *the pedagogical core*. One function that takes the active mode, tool schemas, retrieved RAG chunks, conversation history, and user input, and produces the exact token payload, reporting a per-component token breakdown.
6. **Retriever module** — a loosely-coupled `Retriever` interface (e.g. `Query(ctx, embedding, topK) → ranked chunks`) backed by SQLite. The rest of the engine depends only on the interface, never on the storage backend.
7. **Token metering / observability** — reads `prompt_eval_count` / `eval_count` straight from the serving runner's responses (so the engine never reimplements a tokenizer) and attributes prompt tokens to their sources (system prompt, tools, RAG, history, thinking). Emits events clients render live.
8. **Document store + versioning** — owns the document, its block structure, and its full edit history. **git** owns version history; **SQLite** owns metadata, block IDs, and search (see "Storage & the app database").

## Storage & the app database

SQLite (via `modernc.org/sqlite`, pure Go, no CGO) is the local store holding everything non-git, split into per-service files (`app.db`, `index.db`, `meter.db`, `sessions.db`):

- **document metadata** and **stable block IDs** (paragraphs/headings/tables)
- **embeddings** (via `sqlite-vec`, a `vec0` table for KNN)
- **FTS5** full-text index (keyword retrieval)
- **token-metering events** and **conversation history**

Vector search is just *one* feature of this database, alongside FTS5, enabling **hybrid retrieval** (semantic + lexical) — important for academic citation-matching. `git` remains the versioning engine (coarse history + block candidates); SQLite is the query/metadata/vector layer.

The **Retriever** sits behind a Go interface, so the storage backend (SQLite-vec today) is swappable without touching the agent loop, modes, or context assembler.

## Layer 3 — Clients

- **OpenTUI TUI** — panels: markdown editor, chat, live token meter, model/mode switcher, RAG results, diff preview. OpenTUI ships native `Markdown`, `Diff`, and `TextTable` renderables.
- **Tauri markdown editor** — CodeMirror 6 editor (syntax highlighting, large-doc performance), live GFM preview, Mermaid, workspace sidebar, plus assistant affordances: a **popover chat bubble** on selected text (CodeMirror's selection + tooltip API) and **side-by-side candidate views** (`@codemirror/merge`).

Both talk to the *same* API. The Tauri app can keep native file browsing/OS integration, but all edits and versioning go through the engine so history is consistent across clients.

## Deployment targets

The same Vue + CodeMirror frontend runs in two places, and the Go engine is a native binary that can be shipped two ways:

```
        Vue + CodeMirror (one UI codebase)
        ├── capability adapter: Tauri (IPC) | Web (File System Access API)
                    │
        REST + SSE  │  (one contract, codegen'd)
                    ▼
        Go engine ── shipped as: standalone daemon  OR  Tauri sidecar
```

- **Desktop (Tauri)** — the web UI runs in the system WebView; the Rust core provides native file I/O, OS menus, and dialogs. The Go engine can be bundled as a **Tauri sidecar** (the Rust core spawns it on launch), making the app fully self-contained.
- **Web** — the same UI served from a server, talking to the Go engine over the same REST/SSE (self-hosted on your own machine/LAN; going public trades away local-first privacy).
- **TUI** — the OpenTUI terminal client, same contract, no native shell.

The only per-target work is the **capability adapter** (how the UI reaches the filesystem and OS): a Tauri implementation via `invoke()`, and a web implementation via the File System Access API. All app logic lives in the engine, unchanged across targets.

Caveat: the web target implies self-hosting the Go engine somewhere reachable, which partially gives up local-first/privacy — fine on your own machine or LAN, less so on public infrastructure.

## Versioning (Google-Docs-like, git-backed)

Two levels, both owned by the engine:

| Level | What it stores | Mechanism |
| --- | --- | --- |
| **Document history** (coarse) | every accepted edit / autosave | git commits in a repo the engine owns |
| **Block candidates** (fine) | "3 rewrites of this paragraph" | block-ID-keyed alternatives, diffed against the base |

- **Git is the storage engine** — full-text snapshots that are cheap (only deltas stored), with history, revert, and branching for free. "Nicely formatted versions where only small parts changed" is a **word-level diff between two commits**.
- **The Google-Docs feel is a rendering problem** — green insertions, struck-through deletions, per paragraph — produced by the frontend from a diff the engine returns.
- **Stable block IDs** make fine-grained "just this paragraph changed" possible: the document is modeled as structured blocks (paragraphs/headings/tables) with IDs (which is what CodeMirror/ProseMirror document models already do). Git handles coarse history; block IDs handle paragraph-level versions.

Libraries that make this tractable:
- **Go:** `go-git` (pure Go, no CGO) + `gotextdiff` / `go-diff` for diffs.
- **Rust/Tauri:** `git2` (libgit2) or `gix` (pure Rust) + **`similar`** (the diff engine behind `difftastic` — produces genuinely pretty word-level diffs).
- **Frontend:** CodeMirror 6 `@codemirror/merge` (side-by-side diff) and `@codemirror/view` tooltips (popover).

## The core learning surface: what actually costs tokens

Because the context assembler is a first-class, metered module, the system makes the governing levers visible:

- **System prompt & mode** — a fixed cost on every turn.
- **Tool schemas** — every registered tool's JSON schema lives in the prompt; more tools = more overhead.
- **RAG chunks** — the count and size of retrieved passages (chunking strategy directly changes this).
- **Conversation history** — how much prior context is retained, and where it's truncated.
- **Thinking/reasoning tokens** — models that "think" burn hidden tokens.
- **Completion length** — output tokens and how max-output caps interact with edits.

Change one lever and watch the meter move — this is the bottom-up learning loop the system is built around.

## RAG design (outline)

Ingest literature → chunk with a deliberate strategy (chunk size and shape are token levers) → embed with a small local model → store embeddings in `sqlite-vec` and text in FTS5 → on demand, retrieve the top-k relevant passages (hybrid semantic + lexical) → optional rerank → splice into context *with source markers*, so the model can cite and clients can show provenance.

## Modularity principles

- **Everything over REST** — layers are separate processes, each testable in isolation.
- **Modes and tools are data, not code** — fast to add, cheap to experiment with.
- **One observable choke point** (context assembly + metering) — the single place where "what goes into a call" becomes transparent.
- **Contract-first** — the OpenAPI spec is the boundary; every client is generated from it.
- **Interface-first coupling** — the Retriever and document store sit behind Go interfaces, so storage backends are swappable without touching the rest of the engine.

## Notes and tradeoffs

- **TUI vs. markdown editor** — the TUI is the fastest path and ideal for the live token-metering loop; the Tauri editor adds smooth highlighting, live preview, and Google-Docs-style version browsing. Both are thin clients over the same API, so you build the engine once.
- **Renderer choice (OpenTUI)** — TS directly against the core, or via React/Solid (Solid is what OpenCode uses).
- **No Node at runtime** — Tauri ships Rust + system WebView; Bun/Node is build-time only (Vite).
- **Streaming** — SSE with typed events (`token`, `meter`, `candidate`, `diff`, `done`, `error`); a drop-in NDJSON path mirrors the OpenAI-compatible runners if ever needed.
- **File I/O** — hybrid: frontends own on-disk files (native open/save/watch/dialogs); the engine owns versioned content (git + block IDs + SQLite) plus an optional path-watch for the TUI/headless use.
