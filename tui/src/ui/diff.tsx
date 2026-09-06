// DiffPanel — the diff preview (ADR-0013 §1). Renders the `candidate` and
// `diff` SSE events of the active turn: the staged edit and its word-level
// insertions/deletions, plus the accept affordance (key `a`).
//
// The engine's diff events are WordEdit-shaped payloads (blockId +
// insertions/deletions arrays — api/openapi.yaml DiffEvent), not a unified
// patch, so the panel renders them as +/- word lines (presentation only — no
// domain logic).
//
// NOTE: the OpenTUI reconciler breaks on sibling Solid control-flow
// components, so all rows flow through one For over a computed row list.
import { createMemo, For, Show } from "solid-js";
import type { AppStore } from "../state/store";

type Row =
  | { kind: "candidate"; blockId: string; error?: string }
  | { kind: "block"; blockId: string }
  | { kind: "del"; text: string }
  | { kind: "ins"; text: string }
  | { kind: "none" };

export function DiffPanel(props: { store: AppStore }) {
  const s = () => props.store.state();
  const t = () => s().turn;

  const rows = createMemo<Row[]>(() => {
    const out: Row[] = [];
    const candidate = t().candidate;
    if (candidate) {
      out.push({
        kind: "candidate",
        blockId: candidate.blockId ?? "?",
        error: candidate.error,
      });
    }
    for (const diff of t().diffs) {
      const edits = diff.edits ?? (diff.blockId ? [diff] : []);
      for (const edit of edits) {
        if (edit.blockId) out.push({ kind: "block", blockId: edit.blockId });
        for (const word of edit.deletions ?? []) out.push({ kind: "del", text: word });
        for (const word of edit.insertions ?? []) out.push({ kind: "ins", text: word });
      }
    }
    if (out.length === 0) out.push({ kind: "none" });
    return out;
  });

  return (
    <box border title="diff preview" paddingX={1} flexDirection="column">
      <Show when={rows().length > 0} fallback={<text />}>
        <For each={rows()}>
          {(row) => <DiffRow row={row} />}
        </For>
      </Show>
    </box>
  );
}

function DiffRow(props: { row: Row }) {
  const r = props.row;
  if (r.kind === "none") {
    return <text fg="#666">no candidate staged — edits stream here</text>;
  }
  if (r.kind === "candidate") {
    return (
      <box flexDirection="column" marginBottom={1}>
        <text fg="#7EE787">
          candidate for block <b>{r.blockId}</b> — press <b>a</b> to accept
        </text>
        {r.error ? <text fg="#FF7B72">rejected: {r.error}</text> : undefined}
      </box>
    );
  }
  if (r.kind === "block") {
    return <text fg="#666">block {r.blockId}</text>;
  }
  if (r.kind === "del") {
    return <text fg="#FF7B72">- {r.text}</text>;
  }
  return <text fg="#7EE787">+ {r.text}</text>;
}

