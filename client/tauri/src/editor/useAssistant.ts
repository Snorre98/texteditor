// useAssistant — the Tauri editor's AI-surface logic (F8, ADR-0013 §3). This
// composable is the thin seam between the CodeMirror/`<template>` and the store:
// it holds only UI-observable state (mode, draft, the active session) and routes
// every effect through the store's already-correct actions (`createSession`,
// `submitTurn`). It owns no domain logic and no API calls — presentation only.
//
// Two entry points:
//   - askAbout()   — selection-anchored session (ADR-0026 §1), reuses today's
//                    `createSession(blockId)` → `submitTurn` semantics.
//   - sendDocChat()— doc-level free-form chat; no anchor (ADR-0026 §1).
//
// It is pure Vue reactivity (`ref`/`computed`), so it is unit-testable under
// `bun test` with no DOM (tests/assistant.test.ts).
import { computed, ref } from "vue";
import type { AppStore, MeterTally, TurnState } from "../state/store";
import type { DoneEvent } from "../generated/types.gen";
import { blockIndexAt, blockRanges } from "./blocks";

export interface SelectionRange {
  from: number;
  to: number;
}

export interface AssistantOptions {
  /** The editor's current document text (read at call time). */
  getDocText: () => string;
  /** The editor's current selection range, or null when none. */
  getSelection: () => SelectionRange | null;
}

/** Whether a CodeMirror selection is non-empty (drives the toolbar button). */
export function hasSelectionFor(sel: { empty: boolean }): boolean {
  return !sel.empty;
}

export type TurnStatus = "working" | "done" | "error" | "idle";

/** The turn's lifecycle state, for the chat's status row. */
export function turnStatus(turn: TurnState | undefined): TurnStatus {
  if (!turn) return "idle";
  if (turn.error) return "error";
  if (turn.active) return "working";
  if (turn.done) return "done";
  return "idle";
}

/** A one-line completion summary from the terminal done payload. */
export function turnSummary(done: DoneEvent | null | undefined): string {
  if (!done) return "";
  const model = done.usedModel ? ` with ${done.usedModel}` : "";
  return done.degraded ? `answered${model} (degraded)` : `answered${model}`;
}

/** Sum of the cumulative tally — the turn's total token count. */
export function totalTokens(cumulative: MeterTally | undefined): number {
  if (!cumulative) return 0;
  return Object.values(cumulative).reduce((a, b) => a + b, 0);
}

const PROVISION_HINT =
  "provision the mode's default model — see macos-dev-config/models.json";

const PROVISION_CODES = new Set([
  "model-not-found",
  "no-model-available",
  "daemon-unreachable",
]);

/** Append a one-line provisioning hint for provisioning-shaped error codes. */
export function errorDisplay(error: {
  code?: string;
  message?: string;
}): string {
  const base = error.message ?? "";
  if (error.code && PROVISION_CODES.has(error.code)) {
    return base ? `${base} (${PROVISION_HINT})` : PROVISION_HINT;
  }
  return base;
}

export function createAssistant(store: AppStore, opts: AssistantOptions) {
  const selectedMode = ref<string>(store.state.modes[0]?.name ?? "proofreader");
  const draft = ref<string>("");
  const activeSessionId = ref<string | null>(null);
  const error = ref<string | null>(null);

  const activeSession = computed(() =>
    activeSessionId.value
      ? store.state.sessionStates[activeSessionId.value]
      : undefined,
  );

  const activeTurn = computed(() => activeSession.value?.turn);

  const busy = computed(() => activeSession.value?.turn.active ?? false);

  const messages = computed(() => activeSession.value?.messages ?? []);

  async function askAbout() {
    const sel = opts.getSelection();
    if (!sel) return;
    const docText = opts.getDocText();
    const idx = blockIndexAt(blockRanges(docText), sel.from);
    const blockId = idx >= 0 ? store.state.blocks[idx]?.id : undefined;
    const selected = docText.slice(sel.from, sel.to);
    await runTurn(blockId, undefined,
      selected ? `Improve this selection:\n\n${selected}` : "Improve this selection");
  }

  async function sendDocChat() {
    const input = draft.value.trim();
    if (!input) return;
    draft.value = "";
    await runTurn(undefined, selectedMode.value, input);
  }

  async function runTurn(
    anchorBlockId: string | undefined,
    modeType: string | undefined,
    userInput: string,
  ) {
    if (!store.state.document) return;
    const modeName = selectedMode.value;
    try {
      const sessionId = await store.createSession(anchorBlockId, modeType);
      activeSessionId.value = sessionId;
      await store.submitTurn({
        sessionId,
        modeName,
        documentId: store.state.document.id,
        userInput,
      });
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e);
    }
  }

  return {
    selectedMode,
    draft,
    error,
    activeSessionId,
    activeTurn,
    busy,
    messages,
    askAbout,
    sendDocChat,
  };
}
