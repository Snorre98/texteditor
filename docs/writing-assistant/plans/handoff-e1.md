# Handoff — Phase E1: Ratatui transport + walking skeleton (ADR-0046)

Prompt for a fresh session. Repo: `~/Documents/Liv/Projects/texteditor`
(real path `/Volumes/Ex-SSD/Documents/Liv/Projects/texteditor`).

Status: Phases A–C (C1–C4) landed and green. **Phase D is in flight in a
parallel session; C5 (ADR-0051) is not landed.** E1 consumes neither: it builds
the client transport and a chat loop against the current C4 contract. Do not
touch `client/tui/` (frozen) or `client/tauri/` (frozen, Rust generated tree
untouched). Do not commit unless asked.

## Read first (in order)
1. `docs/writing-assistant/adr/0046-ratatui-tui-v2.md` — the spec you implement
   (§1–4, §7, §10; §5/§6/§8/§9 are E2/E3).
2. `docs/writing-assistant/plans/implementation-sequence-context-engine.md` —
   Phase E1 only.
3. `docs/writing-assistant/adr/0031` (SSE framing), `adr/0021` (discovery/port),
   `adr/0047` (approve = the write boundary), `adr/0050` (reader is E2; do not
   build it here).
4. `docs/writing-assistant/behaviors/client-swap.feature` (the Ratatui scenario).
5. Current code: `api/openapi.yaml`, `client/tauri/openapi-to-rust.toml`,
   `client/tauri/src-tauri/Cargo.toml` + `generated/REQUIRED_DEPS.toml`,
   `client/tui/src/api/{sse,discovery}.ts` (the framing/discovery semantics to
   port), `docs/contribute.md` (codegen/test conventions).

## Scope
Phase E1 only. No corpus tree, no context tray, no reader pane, no locate UI,
no C5 labels, no engine changes. If Phase D lands mid-session, ignore its new
`locate` event (the decoder labels unknown events) — do not build UI for it.

## Pinned decisions (do not relitigate; record deviations with rationale)
1. **Crate** `client/tui-rs/`: Ratatui + crossterm, plain `cargo`, no Tauri CLI,
   no Node/Bun. It is a client sibling under `client/` (ADR-0034).
2. **Regenerated Rust client.** Copy/adapt `client/tauri/openapi-to-rust.toml`
   into the crate (own `spec_path = "../../api/openapi.yaml"`, own
   `output_dir = "src/generated"`, module `gen`, `enable_async_client = true`).
   Regenerate with `cargo install --locked openapi-to-rust` (v0.15.0) +
   `openapi-to-rust generate -c <toml>`; commit the generated tree. This fixes
   the stale `/fleet` gap and brings in C1–C4 routes. Tauri's tree stays
   untouched.
3. **Dependencies**: generated deps (`reqwest 0.13` with `query,rustls,stream`,
   `reqwest-middleware`, `bytes`, `futures-util`, `serde`, `serde_json`,
   `thiserror`) plus `ratatui`, `crossterm`, `tokio`
   (`rt-multi-thread,macros,time,sync`). No `tui-markdown` yet (E2). No new
   dependency beyond these.
4. **Rust SSE decoder** per ADR-0031: split on `\n\n`, parse `event:` +
   multi-line `data:`, skip comments, dispatch by event name to the generated
   payload types (`token, meter, candidate, diff, rag, context, done, error,
   backpressure`), label unknown/invalid and continue, stop at `done`/`error`.
   It must treat future `locate`/`thinking` events as unknown, never crash.
5. **Discovery**: `ENGINE_URL` > numeric `ENGINE_PORT` >
   `http://127.0.0.1:9100`; `GET /health` probe; adopt `baseUrl` when present.
   The generated `HttpClient` supports `with_base_url`.
6. **Async bridge**: one tokio runtime on a worker thread owns generated calls
   and the `/turn` byte stream; the UI thread runs a synchronous crossterm
   event/render loop and receives typed events over an `mpsc` channel. Render
   from render-only snapshots; no domain logic client-side (ADR-0013 §3).
7. **UI v1**: chat pane with streaming text, preset tabs (`GET /modes`; send
   `Task.modeName`), diff preview from the `diff`/`candidate` events, approve
   (`GET candidates` → `POST /documents/{id}/edits` → `POST /documents/{id}/commits`),
   status line: connection state, resolved model (`done.usedModel`/degraded),
   target file path, write-through (`writtenThrough`) or conflict
   (`file-changed-externally`) label. Bracketed paste on (crossterm) — it is
   required by `/locate` in E2. No editor panel, no manual save (ADR-0047).
8. **Bootstrap**: open a document (`POST /documents`), create/resume a session
   (`POST /sessions`, `GET /sessions?documentId=`), then `POST /turn`.
   `Task.workspaceId` is optional; the engine's canonical-parent fallback
   covers pre-workspace flows. Keep the client's state minimal (document id,
   session id, turn state, last events).
9. **Build**: `tools/build-tui-rs.sh` — plain `cargo build`; resolve the repo
   root from `BASH_SOURCE`; document (never auto-set) the external-SSD
   `RUSTUP_HOME`/`CARGO_HOME` exports (ADR-0043 §4 convention). A `--release`
   flag and a `--test` gate are welcome but the gate lands in E3.
10. **No contract changes.** E1 adds no routes/fields/events; the spec is only
    consumed. If you believe one is needed, stop and report instead.
11. Keep the frozen OpenTUI client compiling; do not regenerate anything under
    `client/tui/` or `client/tauri/` in this phase.

## Checkpoints
- **E1a — crate + generated client + discovery**: crate builds, generated tree
  committed, `/health` discovery works against a running engine (unit tests for
  URL precedence + probe).
- **E1b — SSE decoder + async bridge + chat**: decoder unit tests (framing,
  multi-line data, unknown event, terminal); a turn streams into the chat pane.
- **E1c — interaction + tests + smoke**: preset tabs, candidate/diff, approve
  with write-through/conflict status, bracketed paste; `cargo test`,
  `cargo fmt`, `cargo clippy` (if available) green; manual live-model run.

## Tests (required)
- Rust unit tests: SSE framing + dispatch (incl. unknown event), discovery
  precedence/probe, state reduction (token accumulation, done/degraded,
  candidate/conflict labels).
- Manual live gate (engine + control daemon + a model up): open a markdown
  file, chat, receive a candidate, approve, verify the file on disk changed;
  force an external change mid-turn and verify the conflict label.

## Verify
```
cd client/tui-rs && cargo test && cargo fmt --check
./tools/build-tui-rs.sh
```
Plus the manual live gate above. Do not break `cd server && CGO_ENABLED=0 go test ./...`
or the frozen clients (they are untouched).

## Report
Files changed, tests added, exact commands run, pinned-decision deviations with
rationale, and what E2 needs to know: the crate module layout, how the SSE
decoder labels unknown events, the async-bridge channel shape, and any
friction with `openapi-to-rust` (so E2's regeneration is uneventful).
