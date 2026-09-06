// ChatPanel — session-anchored message bubbles plus the live answering-phase
// token stream (ADR-0013 §1, ADR-0026 sessions). Messages come from
// /sessions/{id}/messages; the streaming tail is the store's turn.tokens
// signal, fed by the `token` SSE event.
//
// NOTE: the OpenTUI reconciler breaks when two Solid control-flow components
// (Show/For) are siblings (their insert markers mount as orphan text nodes),
// so each Show lives in its own wrapper box inside the scrollbox.
import { For, Show } from "solid-js";
import type { AppStore } from "../state/store";

export function ChatPanel(props: { store: AppStore }) {
  const s = () => props.store.state();
  const streaming = () => s().turn.active && s().turn.tokens.length > 0;

  return (
    <box flexGrow={1} border title="chat" flexDirection="column">
      <scrollbox
        flexGrow={1}
        stickyScroll
        stickyStart="bottom"
        contentOptions={{ flexDirection: "column", paddingX: 1 }}
      >
        <box flexDirection="column">
          <box flexDirection="column">
            <Show
              when={s().messages.length > 0}
              fallback={
                <text fg="#666">no messages — say something to the engine</text>
              }
            >
              <For each={s().messages}>
                {(msg) => (
                  <box flexDirection="row" gap={1} marginBottom={1}>
                    <text
                      fg={msg.role === "user" ? "#58A6FF" : "#A5D6FF"}
                      width={10}
                    >
                      <b>{msg.role}:</b>
                    </text>
                    <text>{msg.content}</text>
                  </box>
                )}
              </For>
            </Show>
          </box>
          <box flexDirection="column">
            <Show when={streaming()} fallback={<text />}>
              <box flexDirection="row" gap={1} marginBottom={1}>
                <text fg="#A5D6FF" width={10}>
                  <b>assistant:</b>
                </text>
                <text fg="#7EE787">{s().turn.tokens}</text>
              </box>
            </Show>
          </box>
        </box>
      </scrollbox>
    </box>
  );
}

// The chat input row — the single line that submits a turn (POST /turn SSE).
export function ChatInput(props: {
  store: AppStore;
  modeName: string;
}) {
  const store = props.store;

  const submit = (value: string) => {
    const s = store.state();
    if (!s.document || !s.currentSessionId) return;
    void store.submitTurn({
      sessionId: s.currentSessionId,
      modeName: props.modeName,
      documentId: s.document.id,
      userInput: value,
    });
  };

  return (
    <box border paddingX={1}>
      <input
        value=""
        placeholder={
          props.store.state().document
            ? "ask the engine… (enter to send)"
            : "open a document first"
        }
        onSubmit={(value: unknown) => {
          if (typeof value === "string") submit(value);
        }}
      />
    </box>
  );
}
