# Handoff — Phase E2: Context surfaces + engine additions (ADR-0046/0048/0049/0050/0051)

Prompt for a fresh session. Repo: `~/Documents/Liv/Projects/texteditor`
(real path `/Volumes/Ex-SSD/Documents/Liv/Projects/texteditor`).

Status: E1 landed (crate `client/tui-rs/`, regenerated Rust client, SSE decoder,
async bridge, chat/tabs/approve). Phase D and Phase C5 landed. Do not touch
`client/tui/` (frozen in-tree) or `client/tauri/` (frozen). Do not commit unless
asked.

## Read first (in order)
1. `docs/writing-assistant/plans/implementation-sequence-context-engine.md` —
   Phase E2 only.
2. `docs/writing-assistant/adr/0049-context-management-corpus-tray.md`
   (workspaces, corpus, tray, session policy), `adr/0050-tui-reader-pane.md`
   (reader), `adr-0051` (thinking/budget labels), `adr-0048` (locate),
   `adr-0046` (§5 UI surface).
3. Behaviors: `context-management.feature`, `context-inspector.feature`,
   `locate-anchor.feature`, `context-budgets.feature`, `workspace.feature`.
4. The Phase D report (its picker wire shape) and the C5 report (thinking/budget
   fields) — plus the authoritative `api/openapi.yaml`.
5. Current code: `client/tui-rs/` (E1 layout), `server/internal/{loop,apiserver,session}`,
   `api/openapi.yaml`.

## Scope
Phase E2 only. No E3 work (fleet orchestration, daemon fixes, parity/retirement
are E3).

## Deliverables
1. **Engine additions (OpenAPI-first, contract before client):**
   - Session titles: optional `title` on `CreateSessionRequest` + a rename route
     (e.g. `PUT /sessions/{id}`); sessions.db already carries `title`.
   - Cancel generation: `POST /turns/{id}/cancel` with a turn-scoped cancel
     registry in the loop; the turn ends with a labeled terminal outcome
     (e.g. `done {cancelled:true}` or a typed `turn-cancelled` error) and
     partial usage is metered. Typed 404 unknown turn / 409 not running.
   - Tests at the store/loop/apiserver layers; regenerate ogen + Hey API/Zod;
     Tauri Rust untouched.
2. **Workspace + sessions UI:** open/resume a workspace (`GET/POST
   /workspaces`), bounded directory browser (`GET /directories`; render the
   typed `path-outside-allowed-roots` refusal, never swallow it),
   workspace-scoped session list/resume (`GET /sessions?workspaceId=`), title
   rename, cancel action.
3. **Reader pane** (ADR-0050): fetch `GET /documents/{id}/blocks`, join
   `Block.Text` with `\n\n`, render via `tui-markdown`, toggleable beside chat;
   keep the `Block[]` view-model and leave `PUT /documents/{id}/tree` unwired.
4. **Inspector panels:** meter (`meter` event + `GET /sessions/{id}/meter`),
   RAG/context (`rag` + `context` events; render snapshot messages, chunks,
   drops, budget, `pinned`/`humanOverride` labels).
5. **Context tray:** pin/remove, exclude, editable retrieval query, auto-RAG
   toggle; persist via `PUT /sessions/{id}/context` (pin-for-session) and send
   per-turn `Task.context` overrides; the engine re-assembles/re-meters.
6. **Corpus tree:** multi-root scope, per-document status, index/evict/rebuild
   (`GET/PUT /corpus`, `POST /corpus/index`, `DELETE /corpus/documents/{id}`);
   render job progress.
7. **Mentions + paste:** `@`-mention picker over the bounded directory listing
   (`Task.mentions`); bracketed paste throughout.
8. **Locate UI** (D): render the `locate` event; ambiguity picker via
   `POST /turns/{id}/locate`; anchored-turn status (the resolved path/block).
9. **C5 labels:** thinking indicator (`thinking` event), window/budget/
   compaction labels, latency/token readouts from the measurements.
10. **Regenerate the tui-rs Rust client** from the now-current spec (D + C5 +
    E2 additions); commit the generated tree. Keep the frozen OpenTUI TS client
    compiling via its own regen.

## Checkpoints
- **E2a — engine additions + workspace/sessions**: title + cancel routes with
  tests; workspace/session UI working against a live engine.
- **E2b — reader + inspector + tray**: reader pane, meter/context panels, tray
  persistence and per-turn overrides, all manually exercised.
- **E2c — corpus + mentions + locate + C5 labels**: corpus tree, mentions,
  locate picker, thinking/budget labels; polish; gates green.

If the session runs short, stop after a fully green checkpoint and report.

## Tests / gates
- Rust unit tests for each new state reduction (tray merge, corpus status
  transitions, cancel, locate picker, C5 labels).
- Manual vault run: workspace → corpus scope/index/status/evict → tray
  pin/exclude/query/auto-RAG → reader → meter/context → mention → locate →
  cancel; verify write-through still only happens on approve.
- `cargo test`/`cargo fmt`; engine `go test`/vet/gofmt; `client/tui` bun
  test/typecheck after TS regen; Tauri untouched.

## Constraints
Dumb client (ADR-0013 §3): all state engine-side; the TUI renders and toggles.
No editor/manual save (ADR-0047). No client-side provenance/budget computation.
OpenAPI-first for every addition. Keep the reader's write path unwired.

## Report
Files changed, tests added, commands run, deviations with rationale, and what
E3 needs: the parity checklist status, any unexercised surface, and the exact
fleet/orchestration gaps the TUI still compensates for.
