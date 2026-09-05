# Implementation Sequence — Future (Mandatory)

Track 2 of the roadmap, referenced from
[`implementation-sequence.md`](implementation-sequence.md). These phases are
**required, not optional**: the TUI (Plan C) is the proof-of-concept accelerator —
the fastest path to a working system with a live token meter (ADR-0013) — but the
shipped product is the engine deployed two ways (standalone daemon *and* Tauri
sidecar) with the rich Tauri markdown editor. Nothing here is "if we get to it";
it is sequenced *after* the POC only because the engine and OpenAPI contract must
be locked first (contract-first, ADR-0002/0017).

This plan references ADRs only as the source of *what* to build; it decides only
the *order*.

---

## Plan E — Deployment & packaging (engine shipped two ways)

Prereq: A4/A5 (the engine and its OpenAPI contract are locked), Plan B (serving
control on the machine). Ships the engine as two artifacts from one codebase.

1. **Standalone daemon** — the single static Go binary run as a local daemon
   (ADR-0003, ADR-0014 §2). No Node/Python at runtime; no CGO (ADR-0003).
2. **Tauri sidecar** — the Rust core spawns the engine as a sidecar child process
   on launch; **stop = SIGTERM, then SIGKILL on timeout** (ADR-0021 §1).
3. **Port policy** — dynamic-by-default (no `ENGINE_PORT` → engine picks a free
   port; the Rust core reads the actual base URL from `/health`+`/models` and
   injects it); fixed via `ENGINE_PORT` for remote/web use (ADR-0021 §1).
4. **Port discovery** — a "where am I" endpoint (+ optional mDNS on the LAN) so
   clients discover rather than assume a port (ADR-0021 §1).
5. **Bind policy** — `127.0.0.1` default; `ENGINE_BIND=0.0.0.0` opts into LAN
   exposure, documented as the privacy trade-off (ADR-0021 §2). The pre-bind gate
   (Plan B item 5) guards this.
6. **Web target** — the same UI served locally, engine self-hosted on the user's
   own machine/LAN; explicit self-hosting caveat, not a public-infrastructure
   default (ADR-0014, ADR-0021 §2).
7. **Capability adapter (per target)** — the single per-target seam: Tauri `invoke`
   IPC vs Web File System Access API; all app logic stays in the engine, unchanged
   (ADR-0014).

---

## Plan F — Tauri markdown editor (the shipped client)

Prereq: Plan E (sidecar shipped) and the locked OpenAPI spec. Layers 6→8 for the
second client.

### F6 · Client API + DTOs (Rust)

1. Generate the Rust client from the same OpenAPI spec with **`openapi-to-rust`**
   (locked now, not deferred) — ADR-0017 §3, ADR-0003. The spec models streaming in
   standard OpenAPI (`text/event-stream`), no ogen-only extensions, so this tool
   consumes the exact spec the Go/TS clients do (ADR-0017 §3).
2. `tauri-typegen` — deferred; covers Tauri's internal Rust↔JS IPC, not the Go API
   (ADR-0003).

### F7 · Client state

- Vue 3 reactivity over the generated client; the reactive patterns transfer from
  the TUI's Solid state (ADR-0023, ADR-0013 §2).
- Session-scoped state: multiple selection-anchored bubbles + doc-level chat
  streaming simultaneously (ADR-0026 §1/§4).

### F8 · Client UI

1. **Tauri 2** (Rust core + system WebView) + **Vue 3** + **CodeMirror 6**; no Node
   at runtime (Bun/Node is build-time only) — ADR-0013 §2.
2. **Selection popover chat bubble** via the CodeMirror tooltip API; **side-by-side
   candidates** via `@codemirror/merge` — ADR-0013 §2.
3. **Native file browsing / OS integration** in the Rust core, but all edits and
   versioning go through the engine so history is consistent — ADR-0013 §3.
4. **Manual-edit cadence** — human keystrokes batch into an autosave snapshot on a
   silence interval (10 s / N min), distinct from AI-edit commits (ADR-0020 §1).
5. **Session bubbles** — create-or-resume anchored to a block; reopening a
   selection reopens its session (ADR-0026 §1–§3).

---

## Cross-cutting rules (unchanged from the main plan)

- Standalone + boundary-test every module; pure leaves deterministic (R4/R5,
  ADR-0001).
- The engine is reached only via the OpenAPI contract; the Tauri editor is a **dumb
  client** — no domain logic, generated from the spec (ADR-0013, ADR-0017).
- Serving is only reached via Fleet → daemon (ADR-0025, ADR-0027).

## Milestones

1. E1–E5 → engine ships as standalone daemon *and* Tauri sidecar with dynamic-port
   discovery + localhost bind.
2. E6–E7 → web target + capability adapter.
3. F6–F8 → Tauri editor with selection bubbles, side-by-side candidates, and
   autosave-backed manual editing.

After Track 2, the three targets (TUI, desktop WebView, web) run one engine over
one contract (ADR-0014) — the frontend-swap guarantee realized end to end.
