// MeterPanel — the live token meter (ADR-0013 §1, the POC's core loop):
// per-turn attributed breakdown (the `meter` SSE event, interface.md §6) and
// the per-session cumulative tally. thinkingApprox is always labeled
// (ADR-0024); a degradation substitution is labeled too (done.degraded).
//
// NOTE: the universal/test renderer evaluates JSX child expressions eagerly,
// so nullable fields are dereferenced behind keyed <Show> children (lazy) or
// optional chaining — never `!` inside JSX.
import { For, Show } from "solid-js";
import type { AppStore } from "../state/store";
import type { MeterEvent } from "../generated/types.gen";
import { METER_COMPONENTS } from "../state/store";

export function MeterPanel(props: { store: AppStore }) {
  const s = () => props.store.state();

  return (
    <box border title="token meter" paddingX={1} flexDirection="column">
      <ShowTurnRow store={props.store} />
      <box flexDirection="row" gap={1} flexWrap="wrap">
        <For each={METER_COMPONENTS}>
          {(c) => (
            <text width={14}>
              <b>{c}</b>: {s().turn.cumulative[c]}
            </text>
          )}
        </For>
      </box>
    </box>
  );
}

function ShowTurnRow(props: { store: AppStore }) {
  const s = () => props.store.state();
  const t = () => s().turn;
  const labels = () => {
    const parts: string[] = [];
    if (t().meter?.thinkingApprox) parts.push("thinking approx");
    if (t().done?.degraded) parts.push(`degraded (${t().done?.usedModel ?? "?"})`);
    if (t().backpressure) parts.push("stream dropped (backpressure)");
    return parts.join(" · ");
  };
  return (
    <box flexDirection="row" gap={1}>
      <text fg="#666" width={10}>
        last turn
      </text>
      <Show when={t().meter} fallback={<text fg="#666">—</text>}>
        {(m: () => MeterEvent) => (
          <text>
            completion <b>{m().completion}</b> · total <b>{turnTotal(m())}</b>
            {labels() !== "" ? ` · ${labels()}` : ""}
          </text>
        )}
      </Show>
    </box>
  );
}

function turnTotal(m: {
  system: number;
  tools: number;
  rag: number;
  history: number;
  user: number;
  thinking: number;
  completion: number;
}): number {
  return (
    m.system + m.tools + m.rag + m.history + m.user + m.thinking + m.completion
  );
}
