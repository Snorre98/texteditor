# ADR-0039: Manual saves and accepted edits write through to the opened file

Status: Accepted

Amends: ADR-0020 §2 (the "worktree is what editors read" assumption) and ADR-0038
(the tree-save wire route gains an explicit write-through variant).

## Context

ADR-0020 §2 decided the engine owns a working-tree file that "holds the current
canonical markdown (what editors also read)." That assumed the worktree file is
what other editors read. It is not: documents are opened **by absolute path**
(pick-file dialog, ADR-0013 §3; directory listing, ADR-0035). `Open(path)` reads
the file once, registers `documents.path` as a `UNIQUE` dedup key, and copies the
canonical bytes into the engine-owned worktree (`worktree/<docID>/content.md`).
After open the engine never reads or writes `documents.path` again — both edit
paths (autosave tree-save, ADR-0038; accepted AI candidates via `Commit`,
ADR-0020 §1/§4) write only the worktree and git.

So a user who opens a note from their vault (Obsidian), edits or accepts an AI
proposal, clicks Save, and sees "saved at HH:MM:SS" has changed **only the
engine's private copy**. The file on disk — the one Obsidian renders — is never
touched. The ADR-0020 §2 assumption is false for path-opened documents.

## Decision

1. **The Document store mirrors canonical markdown back to `documents.path`.**
   The worktree remains the single canonical source (ADR-0020 §2) and git the
   append-only history; the opened file is a **write-through mirror**, not a
   second authority. Blocks, history, candidates, and every read path are
   unchanged. This applies to the two commit paths: `Commit` (accepted AI edit)
   and `SaveTree` with `writeThrough: true` (explicit manual save).
2. **Explicit save vs cadence are distinguished.** ADR-0038's tree-save is shared
   by the 10 s silence-timer autosave and the explicit Save button.
   `SaveTreeRequest` gains an optional `writeThrough` boolean (default `false`).
   The client sets it `true` on explicit Save / Cmd+S and on the close-flush; the
   periodic autosave leaves it `false`, so mid-typing snapshots stay engine-
   internal and never churn the disk file every 10 s. This preserves ADR-0038's
   model: the client holds the cadence, the engine decides the commit.
3. **Write-back is atomic, ordered, and failure-clean.** Under the existing
   per-document lock, the original file is written before the worktree write, DB
   rewrite, and git commit, via temp-file + rename in the target directory
   (preserving the existing file mode). A write-back failure aborts the save —
   nothing is committed — so the tree stays dirty and a retry re-attempts the
   whole save. The engine and the file never diverge silently.
4. **No-op saves never touch the file.** An unchanged tree already short-circuits
   to the current HEAD (ADR-0038); write-back is skipped. `Commit` with nothing
   applied likewise skips the mirror. The mirror itself also compares bytes and
   skips when the file already holds the canonical content, so re-saving or an
   empty accept never rewrites an unchanged file.
5. **Clients stay dumb.** The write happens engine-side; TUI, Tauri, and web only
   pass the flag (ADR-0013 §3, ADR-0014). No client-side file I/O is added.

## Consequences

- **+** The file the user opened actually changes; Save and Accept behave like a
  real editor, uniformly across every client.
- **+** The engine remains the single source of truth; the worktree↔git
  consistency model (ADR-0020 §2) is untouched, and write-through is a bounded,
  testable side effect at the single write boundary (ADR-0029).
- **−** The written bytes are the engine's canonical serialization (parse →
  format → `serializeBlocks` join), so the file may be lightly normalized on save
  (blank-line runs, trailing newline) relative to the keystrokes.
- **−** Write-through overwrites whatever is on disk at `documents.path`. The
  engine does not re-read the file after open, so external edits made after open
  are clobbered (accepted for now; an mtime/hash pre-check is future work).
- **−** `SaveTreeRequest` gains a field — an OpenAPI change with the three-codegen
  lockstep (ogen Go server, Hey API + Zod TS, `openapi-to-rust`).
- **−** Save now has an additional failure mode (target dir unwritable), surfaced
  as a normal save error.

## Alternatives considered

- **Always write through on every tree save (no flag)** — rejected: the 10 s
  autosave would rewrite the disk file continuously while typing, defeating the
  explicit-vs-cadence distinction.
- **Client-side write-back (Tauri Rust `fs::write` the picked path)** — rejected:
  splits canonical-bytes authority away from the engine (ADR-0020 §2, ADR-0013 §3),
  leaves TUI/web out, and can diverge from worktree/git.
- **Make the worktree file live at the original path (no copy)** — rejected:
  couples engine storage to arbitrary vault locations and breaks the self-contained
  data dir; copy-on-open + write-through mirror keeps storage engine-owned.
- **Detect + warn on external changes (mtime/hash guard)** — deferred: correct but
  larger; recorded as future work above.