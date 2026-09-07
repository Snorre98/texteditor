// chat-window.feature "Assistant messages render sanitized markdown" (ADR-0041
// §4): markdown renders as formatted HTML, and a script tag never executes.
// DOMPurify needs a DOM, so this file registers happy-dom's globals first.
import { describe, expect, test } from "bun:test";
import { GlobalRegistrator } from "@happy-dom/global-registrator";

GlobalRegistrator.register();

const { renderMarkdown } = await import("../src/chat/chatMarkdown");

describe("renderMarkdown", () => {
  test("renders basic markdown formatting", () => {
    const html = renderMarkdown("**bold** and `code`");
    expect(html).toContain("<strong>bold</strong>");
    expect(html).toContain("<code>code</code>");
  });

  test("strips script tags", () => {
    const html = renderMarkdown("hello\n\n<script>window.pwned = true</script>");
    expect(html).not.toContain("<script");
    expect(html).toContain("hello");
  });

  test("strips inline event handlers", () => {
    const html = renderMarkdown('<img src="x" onerror="alert(1)">');
    expect(html).not.toContain("onerror");
  });

  test("keeps lists and links", () => {
    const html = renderMarkdown("- one\n- two\n\n[link](https://example.com)");
    expect(html).toContain("<li>one</li>");
    expect(html).toContain('href="https://example.com"');
  });
});
