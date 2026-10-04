# ADR-0046: TUI v2 — standalone Ratatui client (Rust), replacing OpenTUI

Status: Accepted

Supersedes: ADR-0023 (OpenTUI renderer — Solid).

Amends: ADR-0013 §2 (the TUI's technology), ADR-0017/ADR-0044 (Rust codegen is
unfrozen for this client; Tauri/web remain frozen), ADR-0034 (one more client
sibling under `client/`).

Extends: ADR-0002/ADR-0013 §3 (dumb clients, generated from the contract),
ADR-0031 (hand-framed SSE, per-event typed schemas), ADR-0021 (discovery/port
policy), ADR-0043 (build-script conventions).

## Context

The OpenTUI/Solid TUI is the only actively developed client (ADR-0044) and it
is not holding up:

- Renderer brittleness: the OpenTUI reconciler breaks on sibling control-flow
  components, forcing wrapper boxes and single-`For` rewrites (`chat.tsx`,
  `diff.tsx`); the test renderer dereferences eager children behind `Show`
  (`meter.tsx`, `rag.tsx`); the markdown worker races the test renderer
  (`tests/ui.test.tsx`).
- Functional gaps: the editor panel is read-only, there is no save affordance
  and `saveDocument` is not even wrapped; no paste handling; no session list,
  no cancel; the global `a` key accepts a staged candidate even while typing in
  the chat input.
- The user's verdict after real use: not usable.

Rust/Ratatui is the requested replacement. The repo already generates a Rust
client from the same OpenAPI spec for the Tauri app (`openapi-to-rust`), and
that generated tree is Tauri-free and compiles under default features — so a
standalone Rust TUI can reuse the codegen path rather than inventing a new one.

Forces:

- Dumb-client principle (ADR-0013 §3): all logic/state engine-side; the client
  renders and streams. A Ratatui client must regenerate its types, never
  hand-shape them.
- Tauri is frozen (ADR-0044) but its codegen is the reuse path; unfreezing Rust
  codegen **for the TUI client only** is the minimal governance change. Tauri
  itself stays frozen.
- The committed Rust client is stale: it predates ADR-0040 and has no
  `GET /fleet`, no `FleetState`/`FleetModel` types.
- No Rust SSE decoder exists anywhere in the repo; ADR-0031 requires per-event
  typed framing and no codegen tool provides it.
- The generated calls are async (reqwest 0.13); Ratatui is synchronous — an
  event-loop bridge is required.
- The Rust toolchain lives on the external SSD; build ergonomics must be
  documented, never auto-exported (ADR-0043 §4 convention).
- Fleet orchestration is duplicated in both current client stores; ADR-0040's
  recorded note commits to moving it engine-side so the new client does not
  copy it.

## Decision

1. **New client: `client/tui-rs/`.** A standalone Rust binary built with
   Ratatui + crossterm, plain `cargo`, no Tauri CLI, no sidecar, no Node/Bun.
   It connects to the engine over the existing contract.

2. **Regenerated Rust client.** The new crate owns its own
   `openapi-to-rust.toml` and committed `src/generated/` tree, regenerated from
   `api/openapi.yaml` (which also fixes the missing `/fleet` surface). This
   unfreezes Rust codegen for the TUI only; Tauri/web stay frozen and their
   generated trees stay untouched until an unfreeze decision (ADR-0044).

3. **Rust SSE decoder.** A hand-written decoder over `start_turn`'s byte
   stream implements ADR-0031: split on `\n\n`, parse `event:`/multi-line
   `data:`, skip comments, dispatch by event name to the generated payload
   types (`token`, `meter`, `candidate`, `diff`, `rag`, `done`, `error`,
   `backpressure`), label unknown/invalid events and continue, stop at the
   terminal event.

4. **Async bridge.** A tokio runtime on a worker thread owns generated async
   calls and the stream; the UI thread receives typed events over a channel and
   renders from render-only snapshots. No domain logic in the client.

5. **UI surface.** Chat with streaming, preset **tabs** (ADR-0045), live token
   meter, RAG/context panel, diff preview with approve, and a status line that
   shows the target file and the write-through/conflict state (ADR-0047). **No
   editor panel and no manual save** — the approve action is the write
   boundary (ADR-0047).

6. **Bracketed paste.** crossterm bracketed-paste support, so multi-line
   pastes work — required by `/locate` (ADR-0048).

7. **Discovery.** `ENGINE_URL` > numeric `ENGINE_PORT` >
   `http://127.0.0.1:9100`, verified with `GET /health`; adopt the advertised
   `baseUrl` when present (ADR-0021).

8. **Fleet rendering only.** Fleet orchestration moves engine-side (ADR-0040
   recorded note) before or with this client; the TUI renders `/fleet` and
   issues raw lifecycle verbs, never the busy/poll/switch logic the current
   stores duplicate.

9. **OpenTUI retirement.** The TypeScript TUI is frozen immediately (no new
   features) and retired once the Ratatui client passes a parity checklist
   (chat stream, tabs, meter, RAG/context panel, diff/approve, write-through
   status, fleet render, paste). Its generated Hey API client and tests remain
   until removal.

10. **Build.** `tools/build-tui-rs.sh` (plain `cargo build`, repo-root
    resolution from `BASH_SOURCE`, no machine paths hardcoded; the external-SSD
    `RUSTUP_HOME`/`CARGO_HOME` exports are documented, never auto-set — ADR-0043
    convention).

11. **Behavior contract updated.** `client-swap.feature`'s "TUI renders via
    Solid" scenario is replaced by the Ratatui equivalent; ADR-0023 is
    superseded and remains in the log.

## Consequences

- **+** A client the user can actually use: robust terminal rendering,
  bracketed paste, no reconciler workarounds, no global-key hazards.
- **+** One generated-client toolchain (Rust) for both the TUI and Tauri;
  spec-first codegen is preserved and the stale `/fleet` gap is fixed.
- **+** No Node/Bun at runtime or build time for the TUI.
- **−** Net-new work: crate, async bridge, SSE decoder, build script, parity
  checklist; Rust toolchain (on the external SSD) becomes a TUI dev
  requirement.
- **−** Rust codegen now serves two clients; every contract change must
  regenerate both committed trees until Tauri is retired or unfrozen.
- **−** Governance churn: supersedes ADR-0023, edits `client-swap.feature`,
  and revises ADR-0044's "Rust codegen stays frozen with Tauri" planning line.
- **−** Two TUI codebases coexist until parity; the OpenTUI client gets no
  fixes in the meantime.

## Alternatives considered

- **Fix the OpenTUI TUI** — rejected: the brittleness is in the renderer and
  its reconciler; the workarounds are already load-bearing, and the user's
  verdict is that it is unusable.
- **Keep TypeScript, switch to Ink/blessed** — rejected: no robustness or
  performance gain, and it abandons the existing Rust codegen investment.
- **Tauri-only (unfreeze the GUI)** — rejected: frozen by ADR-0044, heavy, and
  the user explicitly does not want GUI work.
- **Ratatui with a hand-rolled HTTP/SSE client** — rejected: breaks the
  dumb-client/generated-contract rule (ADR-0013 §3, `client-swap.feature`).
- **Port the Tauri Rust client instead of regenerating** — rejected: it is
  stale (no `/fleet`) and lives inside the Tauri crate; regeneration into the
  new crate is the committed-codegen convention (ADR-0034).
