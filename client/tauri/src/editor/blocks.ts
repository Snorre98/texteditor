// The editor's block-tree serialization helpers (F8). Pure and deterministic.
//
// A "dumb client" boundary note (ADR-0013 §3): the engine owns reconciliation,
// ID minting, formatting, and diffing — the client never does any of those.
// These helpers exist only to serialize the text the user is typing into the
// `BlockWrite[]` the manual-edit route (ADR-0038) expects, and to reconstruct
// the flat markdown the CodeMirror editor renders. The engine's `SaveTree` +
// `parseBlocks` remain the source of truth: on the next read the engine re-parses
// the canonical worktree text and reconciles, so a client-side kind hint here is
// best-effort, not authoritative.
import type { Block, BlockWrite } from "../generated/types.gen";

export type BlockKind = BlockWrite["kind"];

const FENCE_LINE = /^(`{3,}|~{3,})/;
const HEADING_MARKER = /^#{1,6}[ \t]+/;
const LIST_MARKER = /^([-*+]|\d+[.)])[ \t]+/;

// blockTreeToMarkdown joins the block tree's canonical texts into one markdown
// document (the engine joins with a blank line — ADR-0020 §2).
export function blockTreeToMarkdown(blocks: Block[]): string {
  return blocks.map((b) => b.text).join("\n\n");
}

// splitTopLevel splits markdown into top-level block fragments at blank-line
// boundaries, keeping fenced code blocks intact (a fence interior may contain
// blank lines). Mirrors the engine's splitTopLevel so the client's serialization
// agrees with the engine's re-parse.
export function splitTopLevel(source: string): string[] {
  const lines = source.split("\n");
  const blocks: string[] = [];
  let cur: string[] = [];
  let inFence = false;
  let fenceChar = "";
  const flush = () => {
    if (cur.length === 0) return;
    blocks.push(cur.join("\n"));
    cur = [];
  };
  for (const ln of lines) {
    if (FENCE_LINE.test(ln)) {
      const c = ln[0];
      if (!inFence) {
        inFence = true;
        fenceChar = c;
      } else if (fenceChar === c) {
        inFence = false;
      }
      cur.push(ln);
      continue;
    }
    if (!inFence && ln.trim() === "") {
      if (cur.length > 0) flush();
      continue;
    }
    cur.push(ln);
  }
  flush();
  return blocks;
}

// classifyFragment returns the BlockKind of a single block fragment (a
// presentation hint for new blocks; the engine re-classifies authoritatively).
export function classifyFragment(text: string): BlockKind {
  const first = text.includes("\n") ? text.slice(0, text.indexOf("\n")) : text;
  const t = first.replace(/^[ \t]+/, "");
  if (t.startsWith("```") || t.startsWith("~~~")) return "code_fence";
  if (t.startsWith(">")) return "blockquote";
  if (HEADING_MARKER.test(t)) return "heading";
  if (LIST_MARKER.test(t)) return "list_item";
  return "paragraph";
}

// blockRanges returns each top-level block's character range [from, to) in the
// flat markdown, so a CodeMirror selection offset can be anchored to a block
// (selection bubbles, ADR-0026 §1).
export function blockRanges(markdown: string): { from: number; to: number }[] {
  const fragments = splitTopLevel(markdown);
  const ranges: { from: number; to: number }[] = [];
  let pos = 0;
  for (const f of fragments) {
    ranges.push({ from: pos, to: pos + f.length });
    pos += f.length + 2; // the "\n\n" separator
  }
  return ranges;
}

// blockIndexAt returns the index of the block containing the given offset, or
// -1 when the offset is in a separator (no block).
export function blockIndexAt(ranges: { from: number; to: number }[], offset: number): number {
  return ranges.findIndex((r) => offset >= r.from && offset <= r.to);
}

// markdownToBlockWrites splits the (edited) flat markdown into blocks and maps
// each fragment to its prior block ID by position: fragment i reuses blocks[i].id
// (stable IDs), and fragments beyond the prior tree are new blocks with no ID
// (the engine mints, ADR-0020 §3). A shrunken tree simply drops the trailing IDs
// (the engine deletes them).
export function markdownToBlockWrites(markdown: string, blocks: Block[]): BlockWrite[] {
  const fragments = splitTopLevel(markdown);
  return fragments.map((text, i) => {
    const prior = blocks[i];
    const kind = prior?.kind ?? classifyFragment(text);
    const write: BlockWrite = { kind, text };
    if (prior?.id) write.id = prior.id;
    return write;
  });
}
