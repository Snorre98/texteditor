// App — the top-level Solid component (ADR-0023). Layout per ADR-0013 §1:
// markdown editor, chat, live token meter, model/mode switcher, RAG results,
// diff preview. The component tree renders store signals; all effects run
// through the store, never the client directly (dumb client, ADR-0013 §3).
import { createMemo, createSignal, onMount } from "solid-js";
import { useKeyboard, useRenderer } from "@opentui/solid";
import type { AppStore } from "../state/store";
import { ChatInput, ChatPanel } from "./chat";
import { DiffPanel } from "./diff";
import { EditorPanel } from "./editor";
import { MeterPanel } from "./meter";
import { RagPanel } from "./rag";
import { Switcher } from "./switcher";

// The discovery error surface (index.tsx sets this before rendering the
// connection-error screen; ADR-0021 §1: clients discover rather than assume).
export const [connectError, setConnectError] = createSignal("");

export interface AppProps {
  store: AppStore;
  docPath: string | null;
}

export function App(props: AppProps) {
  const store = props.store;
  const renderer = useRenderer();
  const [modeName, setModeName] = createSignal("");

  // Boot: discover the fleet, open the document (if one was given), and
  // create-or-resume a session so the chat is turn-ready.
  onMount(() => {
    void (async () => {
      await store.refreshFleet();
      if (modeName() === "") {
        const first = store.state().modes[0];
        if (first) setModeName(first.name);
      }
      if (props.docPath) {
        try {
          await store.openDocument(props.docPath);
          const sessions = store.state().sessions;
          if (sessions.length > 0) {
            await store.selectSession(sessions[0]!.id);
          } else {
            await store.createSession();
          }
        } catch (err) {
          console.error("boot:", err);
        }
      }
    })();
  });

  useKeyboard((key) => {
    if (key.name === "escape") {
      renderer.destroy();
      return;
    }
    // Accept the latest staged candidate ("a" = accept, diff preview).
    if (key.name === "a") {
      const candidate = store.state().turn.candidate;
      if (candidate?.blockId) {
        void store.acceptCandidate(candidate.blockId);
      }
    }
  });

  const hasDoc = () => store.state().document !== null;

  return (
    <box flexDirection="column" padding={1} gap={1}>
      <Switcher
        store={store}
        modeName={modeName()}
        onModeChange={(name) => setModeName(name)}
      />
      <MeterPanel store={store} />
      <box flexDirection="row" flexGrow={1} gap={1}>
        <box width="50%" flexDirection="column" gap={1}>
          <EditorPanel store={store} />
          <DiffPanel store={store} />
        </box>
        <box width="50%" flexDirection="column" gap={1}>
          <ChatPanel store={store} />
          <RagPanel store={store} />
        </box>
      </box>
      <ChatInput store={store} modeName={modeName()} />
      <box>
        <text fg="#666">
          {hasDoc()
            ? "esc quit · a accept candidate · type in the chat input and enter to turn"
            : "esc quit · pass a document path as an argument or TEXTEDITOR_DOC to open one"}
        </text>
      </box>
    </box>
  );
}

// ConnectionErrorScreen — the explicit "engine unreachable" screen rendered
// when discovery fails.
export function ConnectionErrorScreen() {
  const renderer = useRenderer();
  useKeyboard((key) => {
    if (key.name === "escape" || key.name === "q") {
      renderer.destroy();
    }
  });
  const message = createMemo(() => connectError());
  return (
    <box border padding={1} flexDirection="column" gap={1}>
      <text fg="#FF7B72">
        <b>engine unreachable</b>
      </text>
      <text>{message()}</text>
      <text fg="#666">esc/q quit</text>
    </box>
  );
}
