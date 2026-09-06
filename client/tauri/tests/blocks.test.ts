import { describe, expect, test } from "bun:test";
import {
  blockTreeToMarkdown,
  classifyFragment,
  markdownToBlockWrites,
  splitTopLevel,
} from "../src/editor/blocks";
import type { Block } from "../src/generated/types.gen";

const block = (id: string, kind: Block["kind"], text: string): Block => ({
  id,
  kind,
  position: 0,
  text,
});

describe("splitTopLevel", () => {
  test("splits at blank lines", () => {
    expect(splitTopLevel("a\n\nb\n\nc")).toEqual(["a", "b", "c"]);
  });

  test("keeps fenced code blocks intact", () => {
    const src = "```\nline one\n\nline two\n```\n\nafter";
    expect(splitTopLevel(src)).toEqual(["```\nline one\n\nline two\n```", "after"]);
  });

  test("returns [] for empty/whitespace", () => {
    expect(splitTopLevel("")).toEqual([]);
    expect(splitTopLevel("  \n \n")).toEqual([]);
  });
});

describe("classifyFragment", () => {
  test("detects heading, list, fence, blockquote, paragraph", () => {
    expect(classifyFragment("# Title")).toBe("heading");
    expect(classifyFragment("- item")).toBe("list_item");
    expect(classifyFragment("```js")).toBe("code_fence");
    expect(classifyFragment("> quote")).toBe("blockquote");
    expect(classifyFragment("plain text")).toBe("paragraph");
  });
});

describe("blockTreeToMarkdown / markdownToBlockWrites", () => {
  const tree: Block[] = [
    block("b1", "heading", "# Title"),
    block("b2", "paragraph", "hello world"),
  ];

  test("round-trips an unchanged tree, preserving IDs", () => {
    const md = blockTreeToMarkdown(tree);
    const writes = markdownToBlockWrites(md, tree);
    expect(writes).toEqual([
      { kind: "heading", text: "# Title", id: "b1" },
      { kind: "paragraph", text: "hello world", id: "b2" },
    ]);
  });

  test("a new block past the prior tree has no id (engine mints)", () => {
    const md = "# Title\n\nhello world\n\nnew paragraph";
    const writes = markdownToBlockWrites(md, tree);
    expect(writes[2]).toEqual({ kind: "paragraph", text: "new paragraph" });
    expect(writes[2]?.id).toBeUndefined();
    expect(writes[0]?.id).toBe("b1");
    expect(writes[1]?.id).toBe("b2");
  });

  test("a shrunken tree drops the trailing id", () => {
    const writes = markdownToBlockWrites("# Title", tree);
    expect(writes).toEqual([{ kind: "heading", text: "# Title", id: "b1" }]);
  });
});
