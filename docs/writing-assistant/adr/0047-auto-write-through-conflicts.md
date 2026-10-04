# ADR-0047: Auto write-through on approve with external-change detection

Status: Accepted

Amends: ADR-0039 (write-through semantics: approve becomes the write boundary;
the accepted "clobber external edits" risk is closed), ADR-0029 (guards are
enforced on the model edit path), ADR-0020 §1 (candidate ordering is
deterministic newest-first).

## Context

The user reports that getting an edited file to actually save has been
unreliable. Root causes found in the as-built code:

- **Tauri reverts accepted edits**: after `acceptCandidate`, `Editor.vue` never
  re-renders from the refreshed blocks; the next autosave maps stale editor
  text back by array index and silently overwrites the accepted edit in
  worktree, git, and on disk.
- **`Open` never revalidates**: an existing `documents.path` row returns early
  with no re-read, no mtime/hash check (`store.go:139-144`), so after a restart
  the engine's worktree is stale relative to Obsidian.
- **No-op saves skip write-back**: `SaveTree` short-circuits when canonical ==
  worktree (`store.go:295-309`) even with `writeThrough:true`; if the disk file
  changed externally, the engine never re-syncs — and the next real edit
  clobbers it.
- **Symlinks are destroyed**: `atomicWriteFile` renames over `documents.path`
  (`store.go:608`); a symlinked vault path is replaced by a regular file.
- **Candidate ordering is inconsistent**: the API returns `ORDER BY ts DESC`
  but both clients take `.at(-1)` — the oldest candidate; `ts` is second-
  resolution, so ties are nondeterministic.
- **Guards are dead on the model path**: the `edit_markdown` tool schema has no
  guards and the handler never passes them; `Commit` never re-checks
  `base_rev`, so a stale candidate can be committed over newer content.
- **No E2E test** covers propose → approve → bytes on disk; tests are all
  stubs.

ADR-0039 explicitly accepted the clobber risk as future work and modeled save
as an explicit client action (`writeThrough` flag). The new TUI (ADR-0046) has
no editor and no manual save; approve is the only write boundary, so the
engine must make that boundary safe by itself.

Forces:

- The engine owns the bytes (ADR-0029); clients stay dumb (ADR-0013 §3).
- The worktree remains canonical and git remains the history (ADR-0020).
- Failures must degrade with a label, never silently (Q1/ADR-0022 discipline).
- Contract-first: new response fields and a conflict error are recorded in
  `api/openapi.yaml` before client code; three codegens stay in lockstep
  (Rust regenerated for the new TUI, ADR-0046).

## Decision

1. **Approve = stage + commit + write-through, in one action.** The client
   sends accept; the engine commits and mirrors to `documents.path` before
   answering. There is no manual save in the TUI; the explicit `writeThrough`
   flag remains for tree saves (ADR-0039) and for compatibility.

2. **Open revalidates.** On every open, canonicalize the path
   (`EvalSymlinks` + case-fold) and compare the file's content hash/mtime to
   the engine's last-known hash for that document. A mismatch re-reads the file
   into the worktree and records an `external-change` notice; aliases of the
   same file resolve to one document row.

3. **Write-back is conflict-checked.** Immediately before the atomic rename,
   the engine compares the on-disk hash to the last-known hash. A mismatch
   aborts the write with a typed `file-changed-externally` (409) error carrying
   the current on-disk hash; the client may reload or explicitly overwrite
   (an explicit overwrite flag). Never a silent clobber.

4. **No-op saves re-sync or conflict.** If canonical == worktree but the disk
   differs, the engine does not short-circuit: it either mirrors (no pending
   engine change) or reports the conflict per (3). The silent no-op path is
   removed.

5. **Candidates are newest-first and validated.** Listing and commit use a
   deterministic newest-first order (`ts DESC, rowid DESC`). `Commit`
   re-validates each candidate's base hash and fails stale ones with
   `guard-failed` instead of applying them.

6. **Guards are live on the model path.** The `edit_markdown` tool carries the
   target block's base hash as a guard; the handler passes it to `ApplyEdit`.
   A model edit computed against stale text cannot land.

7. **Symlink-safe writes.** The write target is resolved before the atomic
   temp-file + rename; a symlink is written through, never replaced by a
   regular file.

8. **Contract additions.** The commit response gains `writtenThrough`, `path`,
   and `revision`; `file-changed-externally` is documented as a typed error.
   The commit message is derived (preset · block · diff); empty accepts create
   no commit.

9. **Tests are the acceptance gate.** Store-level tests: symlink, external
   change, no-op re-sync, ordering, stale guard. HTTP-level E2E: open file →
   stubbed turn emits `edit_markdown` → approve → file bytes on disk changed;
   external change between open and approve → 409, no write.

## Consequences

- **+** Approve always means "the file changed" or a labeled conflict; the
  failure mode the user hit disappears by construction.
- **+** External editors (Obsidian) are no longer silently clobbered; the
  conflict is explicit and recoverable.
- **+** Stale edits cannot land (guards + base validation), and candidate
  ordering is deterministic.
- **−** More contract surface (response fields + error code) with three-codegen
  lockstep.
- **−** Hash checks add a read per open/commit (negligible for a local
  single-user engine) and introduce a retry path (reload/overwrite) clients
  must render.
- **−** Reload-on-open discards engine-side divergence; if a future flow stages
  candidates across restarts, the policy needs revisiting.
- **−** Explicit-overwrite is a sharp tool; it must stay opt-in and labeled.

## Alternatives considered

- **Keep explicit save (fix only the Tauri revert bug)** — rejected: the new
  TUI has no editor or save affordance; approve is the natural write boundary,
  and the flag already caused the no-op/conflict gap.
- **Always clobber (status quo)** — rejected: silent data loss against
  Obsidian; ADR-0039 itself listed this as future work.
- **fsnotify watcher + live sync** — deferred: a hash pre-check is sufficient
  at this scale; a watcher adds a dependency and lifecycle complexity.
- **Advisory lockfile** — rejected: Obsidian and other editors do not honor
  engine-side locks; the hash check is the honest mechanism.
- **Client-side write-back** — rejected by ADR-0039 (splits canonical
  authority); nothing here reopens that.
