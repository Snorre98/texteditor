// EditorPanel — the markdown editor view (ADR-0013 §1). Renders the document's
// block tree as markdown text through the native Markdown renderable. The TUI
// editor is read-only in the POC: edits arrive as candidates from the engine
// (diff preview), and accepting them re-reads /blocks (ADR-0013 §3 — all edits
// go through the engine).
import { createMemo } from "solid-js";
import { SyntaxStyle } from "@opentui/core";
import type { AppStore } from "../state/store";

const syntaxStyle = () =>
  SyntaxStyle.fromStyles({
    "markup.heading.1": { fg: "#58A6FF", bold: true },
    "markup.heading.2": { fg: "#58A6FF", bold: true },
    "markup.list": { fg: "#A5D6FF" },
    "markup.raw": { fg: "#A5D6FF" },
    default: { fg: "#E6EDF3" },
  });

export function EditorPanel(props: { store: AppStore }) {
  const s = () => props.store.state();
  const content = createMemo(() =>
    s().blocks.map((b) => b.text).join("\n\n"),
  );
  const title = () =>
    s().document ? `document — ${s().document!.path}` : "document — none open";

  return (
    <box flexGrow={1} border title={title()} flexDirection="column" padding={1}>
      {s().document === null ? (
        <text fg="#666">
          no document open — start the TUI with a path argument
        </text>
      ) : (
        <markdown content={content()} syntaxStyle={syntaxStyle()} flexGrow={1} />
      )}
    </box>
  );
}
