// chatMarkdown — sanitized markdown rendering for assistant message bubbles
// (ADR-0041 §4). `marked` parses, `DOMPurify` sanitizes: a message containing
// markup or a script tag renders as formatted text, and no script executes.
// Raw block-level tag pairs (script/style/iframe/object/embed/form) are
// stripped textually BEFORE sanitization — deterministic belt-and-braces on
// top of DOMPurify's attribute whitelisting (which alone does not remove
// script elements in every DOM implementation, e.g. happy-dom in tests).
import { marked } from "marked";
import DOMPurify from "dompurify";

const RAW_BLOCK_TAGS =
  /<\s*(script|style|iframe|object|embed|form)\b[^>]*>[\s\S]*?<\s*\/\s*\1\s*>/gi;

export function renderMarkdown(md: string): string {
  const html = marked.parse(md, { async: false }) as string;
  return DOMPurify.sanitize(html.replace(RAW_BLOCK_TAGS, ""));
}
