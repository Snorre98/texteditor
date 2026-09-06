# texteditor

A local-first writing assistant. One Go **engine** (REST/SSE, all logic and
state), two **clients**: a terminal **TUI** and a **Tauri** desktop editor. Every
token that goes into a model call is metered and visible.

Serving is reached only through the control daemon in the sibling
[`macos-dev-config`](../macos-dev-config) repo — the engine never reads
`models.json` or starts servers itself. Architecture + status:
[`docs/writing-assistant/`](docs/writing-assistant/architecture.md).

## Prerequisites

- **Go 1.26** (engine, no CGO) · **Bun** (TUI; Tauri build only)
- **Rust** (Tauri only — see [deploy](#deploy-the-tauri-editor))
- The **control daemon** running (engine fails fast at startup without it)
- **Provisioned models** — download at least one before first use (see
  [constraints](#constraints))

## Use the TUI

```sh
# 1. control daemon (sibling repo)
cd ../macos-dev-config && go run ./cmd/fleetdaemon

# 2. engine (pinned port for a stable URL)
cd ../texteditor/server && go run ./cmd/texteditor --port 9100

# 3. TUI — first time only
cd ../texteditor/client/tui && bun install && bun run gen

# 4. open a document and start editing
bun run src/index.tsx /path/to/note.md
```

**What you can do:**

- Chat with your document in any mode — `editor`, `proofreader`, `grammar`,
  `drafter` — type in the chat input and press `enter`.
- Review the AI's edit in the diff preview, press `a` to accept (commits through
  the engine), or keep typing to discard.
- Watch the **live token meter** — every prompt/response cost, per component.
- Switch model/mode from the panels; the engine starts the chosen model via the
  daemon automatically.
- Ask it to retrieve from your notes (`retrieve`/`read_note` tools) — results
  show in the RAG panel with provenance.

**Keys:** `enter` send · `a` accept candidate · `esc` quit.

## Use the Tauri editor

```sh
# 1. build the engine as the bundled sidecar (from the repo root)
cd server && CGO_ENABLED=0 go build \
  -o ../client/tauri/src-tauri/binaries/texteditor-aarch64-apple-darwin \
  ./cmd/texteditor

# 2. deps
cd ../client/tauri && bun install

# 2b — Rust lives on the external SSD; add it to PATH if `cargo` isn't found:
export RUSTUP_HOME=/Volumes/Ex-SSD/caches/rust CARGO_HOME=/Volumes/Ex-SSD/caches/cargo
export PATH="$CARGO_HOME/bin:$PATH"

# 3. dev run (control daemon must be up — the app spawns the engine itself)
bun run tauri:dev
```

**What you can do:**

- Open a file or folder with the native dialog.
- **Select text → a chat bubble appears** — anchored to that block; re-selecting
  the block reopens the same session.
- Review AI edits **side-by-side** (`@codemirror/merge`) and accept.
- Edit by hand — keystrokes autosave as snapshots (every ~10 s), kept separate
  from AI commits.

## Deploy

**Standalone engine daemon** (TUI/web use, launchd, port 9100):

```sh
./tools/build.sh                       # → bin/texteditor (single static binary)
ENGINE_PORT=9100 ./tools/install-daemon.sh
```

**Tauri app** — the shipped desktop bundle, engine included:

```sh
cd client/tauri && bun run tauri:build
```

**Web** — the same UI self-hosted: run the engine with `ENGINE_BIND=0.0.0.0`
(LAN exposure is an explicit opt-in) and serve `client/tauri`'s build output;
see `client/tauri/README.md` for the CORS/ACL details.

## Functionality

- **Modes** — `editor` · `proofreader` · `grammar` · `drafter` (declarative:
  prompt + model + tools, `server/config/modes/`).
- **Tools** — `edit_markdown` (block replace), `retrieve`, `read_note`, `diff`
  (`server/config/tools/`).
- **Versioning** — git-backed document history + per-block AI candidates.
- **Token meter** — per-turn, per-component, per-model.

## Constraints

- The **control daemon must be reachable** — the engine refuses startup otherwise.
- **Provision models first** (`hf download <repo>` in `macos-dev-config`; repos
  in `models.json`) or starting a model fails with `model-not-found`. Big models
  need the SSD mounted and ~18 GB RAM — run one at a time.
- **Localhost by default** (`127.0.0.1`); anything else is explicit opt-in
  (`ENGINE_BIND`, `ENGINE_PORT`, `ENGINE_CORS_ORIGINS`).
- **Clients are dumb** — all edits and versioning go through the engine; there
  is no shared client/engine code.
- The **tool router** (`toolCalling: "router"`) is off — `native` tool-calling is
  the baseline until the deferred fine-tune lands (see
  [`status.md`](docs/writing-assistant/status.md)).

## Developing

Layout, codegen, flags, and the test matrix: [`docs/contribute.md`](docs/contribute.md).
