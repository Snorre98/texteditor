// RagPanel — RAG results (ADR-0013 §1). Consumes the `rag` SSE event, added
// to the event vocabulary as a recorded amendment (ADR-0017 §6, loop emits on
// retrieve/read_note observation). Each chunk shows provenance: blockId,
// score, source.
//
// NOTE: the universal/test renderer evaluates JSX child expressions eagerly,
// so nullable fields are dereferenced behind keyed <Show> children (lazy).
import { For, Show } from "solid-js";
import type { AppStore } from "../state/store";
import type { RagEvent } from "../generated/types.gen";

export function RagPanel(props: { store: AppStore }) {
  const s = () => props.store.state();
  const rag = () => s().turn.rag;

  return (
    <box border title="rag results" paddingX={1} flexDirection="column">
      <Show
        when={rag()}
        fallback={
          <text fg="#666">
            no retrieval streamed yet — a turn with the drafter/editor mode will
            surface chunks here
          </text>
        }
      >
        {(r: () => RagEvent) => (
          <Show
            when={(r().chunks ?? []).length > 0}
            fallback={<text fg="#666">retrieval ran; no chunks returned</text>}
          >
            <For each={r().chunks ?? []}>
              {(chunk) => (
                <box flexDirection="column" marginBottom={1}>
                  <text>
                    <b>{chunk.blockId}</b>
                    {chunk.score !== undefined
                      ? ` · score ${chunk.score.toFixed(3)}`
                      : ""}
                    {chunk.source ? ` · ${chunk.source}` : ""}
                  </text>
                  <text fg="#A5D6FF">{chunk.text}</text>
                </box>
              )}
            </For>
          </Show>
        )}
      </Show>
    </box>
  );
}
