<script setup lang="ts">
// Editor — the CodeMirror 6 editor (F8, ADR-0013 §2). Renders the document's
// block tree as markdown, offers a selection-anchored "Ask about selection"
// toolbar button (ADR-0026; deterministic, mouse + keyboard, ADR-0026 §1),
// side-by-side candidates (@codemirror/merge), doc-level free-form chat, a mode
// selector, and the manual-edit autosave cadence (ADR-0020 §1, ADR-0038). It is
// a dumb client: every edit, session, and versioning action is routed through
// the store to the engine (ADR-0013 §3).
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { markdown } from "@codemirror/lang-markdown";
import type { AppStore } from "../state/store";
import { stepLabel } from "../state/store";
import { capabilityAdapter } from "../capability";
import { isTauri } from "../engine";
import { blockTreeToMarkdown, markdownToBlockWrites } from "./blocks";
import { createAutosave, DEFAULT_AUTOSAVE_INTERVAL_MS, isSaveShortcut, saveStatusLabel } from "./autosave";
import { createAssistant, errorDisplay, hasSelectionFor, totalTokens, turnStatus, turnSummary } from "./useAssistant";
import CandidateMerge from "./CandidateMerge.vue";

const props = defineProps<{ store: AppStore }>();

const container = ref<HTMLElement | null>(null);
const error = ref<string | null>(null);
const hasSelection = ref(false);
const dirty = ref(false);
const savedAt = ref<Date | null>(null);

const assistant = createAssistant(props.store, {
  getDocText: () => view?.state.doc.toString() ?? "",
  getSelection: () =>
    view
      ? { from: view.state.selection.main.from, to: view.state.selection.main.to }
      : null,
});

const { selectedMode, draft, busy, activeTurn, messages, askAbout, sendDocChat } =
  assistant;
const assistantError = assistant.error;

let view: EditorView | null = null;
let autosave: ReturnType<typeof createAutosave> | null = null;
let closeUnlisten: (() => void) | null = null;

// --------------------------- document load + autosave ---------------------------

async function openFile() {
  try {
    const picked = await capabilityAdapter().pickFile();
    if (!picked?.path) return;
    await props.store.openDocument(picked.path);
    replaceDocument(blockTreeToMarkdown(props.store.state.blocks));
    dirty.value = false;
    savedAt.value = null;
    assistant.activeSessionId.value = null;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

function replaceDocument(md: string) {
  view?.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: md } });
}

async function doSave(writeThrough: boolean) {
  if (!props.store.state.document || !view) return;
  try {
    const tree = markdownToBlockWrites(view.state.doc.toString(), props.store.state.blocks);
    await props.store.saveTree(tree, { writeThrough });
    dirty.value = false;
    savedAt.value = new Date();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

// Save now: cancel any pending silence timer and flush immediately.
async function saveNow() {
  if (!autosave) return;
  try {
    await autosave.flush();
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

function onKeydown(e: KeyboardEvent) {
  if (!isSaveShortcut(e)) return;
  e.preventDefault();
  void saveNow();
}

// --------------------------- derived state ---------------------------

const candidateBlockId = computed(() => {
  const cand = activeTurn.value?.candidate;
  return cand && cand.ok ? cand.blockId : undefined;
});

const candidateBaseText = computed(() => {
  const id = candidateBlockId.value;
  if (!id) return "";
  return props.store.state.blocks.find((b) => b.id === id)?.text ?? "";
});

// Fleet observability (ADR-0040 §5): which model currently serves the selected
// mode — its defaultModel when that is up, else the first up model.
const currentModel = computed(() => {
  const fleet = props.store.state.fleet;
  const mode = props.store.state.modes.find((m) => m.name === selectedMode.value);
  const def = mode?.defaultModel;
  const isUp = (name: string) =>
    fleet.models.find((m) => m.name === name)?.liveState === "up";
  if (def && isUp(def)) return def;
  return fleet.models.find((m) => m.liveState === "up")?.name ?? "";
});

const hasMeterData = computed(() => {
  const c = activeTurn.value?.cumulative;
  return c ? Object.values(c).some((v) => v > 0) : false;
});

const saveStatus = computed(() => saveStatusLabel(dirty.value, savedAt.value));

// --------------------------- lifecycle ---------------------------

onMounted(() => {
  if (!container.value) return;

  view = new EditorView({
    parent: container.value,
    state: EditorState.create({
      doc: "",
      extensions: [
        basicSetup,
        markdown(),
        EditorView.lineWrapping,
        EditorView.updateListener.of((u) => {
          if (u.docChanged) {
            autosave?.noteEdit();
            dirty.value = true;
          }
          if (u.selectionSet || u.docChanged) {
            hasSelection.value = hasSelectionFor(u.view.state.selection.main);
          }
        }),
      ],
    }),
  });

  autosave = createAutosave({
    intervalMs: DEFAULT_AUTOSAVE_INTERVAL_MS,
    onSave: doSave,
  });

  window.addEventListener("keydown", onKeydown);

  if (isTauri()) {
    void (async () => {
      const { getCurrentWindow } = await import("@tauri-apps/api/window");
      const appWindow = getCurrentWindow();
      closeUnlisten = await appWindow.onCloseRequested(async (event) => {
        event.preventDefault();
        await autosave?.flush();
        await appWindow.destroy();
      });
    })();
  } else {
    // Web target: best-effort flush when the tab is being hidden/closed. Closing
    // is an explicit save, so it writes through to the opened file (ADR-0039).
    window.addEventListener("pagehide", () => void doSave(true));
  }
});

onBeforeUnmount(async () => {
  closeUnlisten?.();
  closeUnlisten = null;
  window.removeEventListener("keydown", onKeydown);
  // Flush before teardown so the last silence window is not lost on close.
  await autosave?.flush();
  autosave?.dispose();
  autosave = null;
  view?.destroy();
  view = null;
});
</script>

<template>
  <div class="editor">
    <div class="editor__toolbar">
      <button type="button" @click="openFile">Open file</button>
      <button
        type="button"
        :disabled="!hasSelection || !store.state.document || busy"
        @click="askAbout()"
      >
        Ask about selection
      </button>
      <button type="button" :disabled="!store.state.document" @click="saveNow">
        Save
      </button>
      <span v-if="saveStatus" class="editor__save-status" :class="`save-${dirty ? 'dirty' : 'clean'}`">
        {{ saveStatus }}
      </span>
      <span v-if="store.state.document" class="editor__path">
        {{ store.state.document.path }}
      </span>
    </div>
    <p v-if="error" class="editor__error">{{ error }}</p>

    <div ref="container" class="editor__view" />

    <CandidateMerge
      v-if="candidateBlockId"
      :key="candidateBlockId"
      :store="store"
      :block-id="candidateBlockId"
      :base-text="candidateBaseText"
      @reject="activeTurn && (activeTurn.candidate = null)"
    />

    <section class="editor__chat">
      <div class="editor__chat-header">
        <h2>Chat</h2>
        <label class="editor__mode">
          Mode
          <select v-model="selectedMode">
            <option
              v-for="m in store.state.modes"
              :key="m.name"
              :value="m.name"
            >
              {{ m.name }}
            </option>
          </select>
        </label>
        <form class="editor__composer" @submit.prevent="sendDocChat()">
          <input
            v-model="draft"
            type="text"
            placeholder="Ask about the document…"
          />
          <button type="submit" :disabled="!draft || !store.state.document || busy">
            Send
          </button>
        </form>
      </div>

      <section class="editor__fleet" aria-label="Model availability">
        <div class="editor__fleet-head">
          <h3>Models</h3>
          <button
            type="button"
            class="editor__fleet-refresh"
            :disabled="store.state.fleet.busy !== null"
            @click="store.refreshFleet()"
          >
            Check servers
          </button>
        </div>
        <p
          v-if="store.state.fleet.control === 'unreachable'"
          class="editor__fleet-banner"
        >
          serving control unavailable — showing last known models
        </p>
        <ul class="editor__fleet-list">
          <li
            v-for="m in store.state.fleet.models"
            :key="m.name"
            class="editor__fleet-row"
            :class="`state-${m.liveState}`"
          >
            <span class="editor__fleet-dot" :class="`dot-${m.liveState}`" />
            <span class="editor__fleet-name">{{ m.name }}</span>
            <span v-if="currentModel === m.name" class="editor__fleet-current">
              serving
            </span>
            <span class="editor__fleet-url">{{ m.baseUrl }}</span>
            <span class="editor__fleet-actions">
              <span v-if="m.liveState === 'starting' || m.liveState === 'provisioning'">
                {{ m.liveState }}…
              </span>
              <button
                v-else-if="m.liveState === 'up' && currentModel !== m.name"
                type="button"
                :disabled="store.state.fleet.busy !== null"
                @click="store.stopModel(m.name)"
              >
                {{ store.state.fleet.busy === m.name ? "stopping…" : "Stop" }}
              </button>
              <button
                v-else-if="m.liveState === 'down' || m.liveState === 'unknown'"
                type="button"
                :disabled="store.state.fleet.busy !== null"
                @click="store.startModel(m.name)"
              >
                {{ store.state.fleet.busy === m.name ? "starting…" : "Start" }}
              </button>
            </span>
          </li>
        </ul>
        <p v-if="store.state.fleet.error" class="editor__error">
          {{ store.state.fleet.error }}
        </p>
      </section>

      <ol class="editor__messages">
        <li v-for="(m, i) in messages" :key="i" :class="`role-${m.role}`">
          <strong>{{ m.role }}:</strong> {{ m.content }}
        </li>
      </ol>

      <p class="editor__status" :class="`status-${turnStatus(activeTurn)}`">
        <span v-if="turnStatus(activeTurn) === 'working'" class="editor__spinner" />
        <template v-if="turnStatus(activeTurn) === 'working'">Working…</template>
        <template v-else-if="turnStatus(activeTurn) === 'done'">
          Done — {{ turnSummary(activeTurn?.done) }}
          <span v-if="totalTokens(activeTurn?.cumulative) > 0" class="editor__tokens">
            · {{ totalTokens(activeTurn?.cumulative) }} tokens
          </span>
        </template>
      </p>

      <ol v-if="activeTurn?.steps?.length" class="editor__steps">
        <li v-for="(s, i) in activeTurn.steps" :key="i">{{ stepLabel(s) }}</li>
      </ol>

      <p v-if="activeTurn?.active && activeTurn?.tokens" class="editor__stream">
        <span class="editor__stream-label">streaming answer:</span>
        {{ activeTurn.tokens }}
      </p>
      <p v-if="activeTurn?.backpressure" class="editor__warning">
        client buffer overflow — some events were dropped
      </p>

      <div v-if="hasMeterData" class="editor__meter">
        <h3>Meter</h3>
        <ul>
          <li v-for="(val, name) in activeTurn?.cumulative" :key="name">
            <span>{{ name }}</span>
            <span>{{ val }}</span>
          </li>
        </ul>
      </div>

      <div v-if="activeTurn?.rag?.chunks?.length" class="editor__rag">
        <h3>Retrieval</h3>
        <ul>
          <li v-for="(c, i) in activeTurn.rag.chunks" :key="i">
            <span class="editor__rag-source">{{ c.source ?? c.blockId }}</span>
            {{ c.text }}
          </li>
        </ul>
      </div>

      <p v-if="activeTurn?.error" class="editor__error editor__error--turn">
        {{ errorDisplay(activeTurn.error) }}
      </p>
      <p v-if="assistantError" class="editor__error">{{ assistantError }}</p>
    </section>
  </div>
</template>

<style scoped>
.editor {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  max-width: 60rem;
  margin: 0 auto;
}
.editor__toolbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.editor__path {
  font-size: 0.85rem;
  color: #6b7280;
  font-family: monospace;
}
.editor__save-status {
  font-size: 0.8rem;
}
.editor__save-status.save-dirty {
  color: #b45309;
}
.editor__save-status.save-clean {
  color: #047857;
}
.editor__view {
  border: 1px solid #e5e7eb;
  border-radius: 0.375rem;
  min-height: 24rem;
}
.editor__view :deep(.cm-editor) {
  height: 100%;
}
.editor__chat {
  border: 1px solid #e5e7eb;
  border-radius: 0.375rem;
  padding: 0.75rem;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.editor__chat-header {
  display: flex;
  align-items: center;
  gap: 1rem;
  flex-wrap: wrap;
}
.editor__chat-header h2 {
  margin: 0;
}
.editor__mode {
  font-size: 0.85rem;
  color: #4b5563;
}
.editor__fleet {
  border: 1px solid #e5e7eb;
  border-radius: 0.375rem;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
}
.editor__fleet-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.editor__fleet-head h3 {
  margin: 0;
  font-size: 0.9rem;
}
.editor__fleet-refresh {
  font-size: 0.8rem;
  padding: 0.15rem 0.5rem;
}
.editor__fleet-banner {
  margin: 0.35rem 0 0;
  color: #92400e;
  background: #fef3c7;
  border: 1px solid #fcd34d;
  border-radius: 0.25rem;
  padding: 0.25rem 0.5rem;
}
.editor__fleet-list {
  list-style: none;
  margin: 0.4rem 0 0;
  padding: 0;
}
.editor__fleet-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.15rem 0;
}
.editor__fleet-dot {
  width: 0.6rem;
  height: 0.6rem;
  border-radius: 50%;
  flex-shrink: 0;
}
.dot-up {
  background: #047857;
}
.dot-down {
  background: #b91c1c;
}
.dot-starting,
.dot-provisioning {
  background: #b45309;
  animation: editor-pulse 1s ease-in-out infinite;
}
.dot-unknown {
  background: #9ca3af;
}
@keyframes editor-pulse {
  50% {
    opacity: 0.35;
  }
}
.editor__fleet-name {
  font-family: monospace;
}
.editor__fleet-current {
  color: #047857;
  font-weight: 600;
}
.editor__fleet-url {
  color: #9ca3af;
  font-family: monospace;
  font-size: 0.75rem;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.editor__fleet-actions {
  margin-left: auto;
  font-size: 0.8rem;
}
.editor__fleet-actions button {
  font-size: 0.75rem;
  padding: 0.1rem 0.45rem;
}
.editor__composer {
  display: flex;
  gap: 0.5rem;
  flex: 1;
  min-width: 16rem;
}
.editor__composer input {
  flex: 1;
  padding: 0.35rem 0.5rem;
  border: 1px solid #d1d5db;
  border-radius: 0.375rem;
}
.editor__messages {
  margin: 0;
  padding-left: 1.25rem;
}
.editor__status {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin: 0;
  font-size: 0.9rem;
}
.editor__status.status-working {
  color: #2563eb;
}
.editor__status.status-done {
  color: #047857;
}
.editor__spinner {
  width: 0.8rem;
  height: 0.8rem;
  border: 2px solid #bfdbfe;
  border-top-color: #2563eb;
  border-radius: 50%;
  animation: editor-spin 0.8s linear infinite;
}
@keyframes editor-spin {
  to {
    transform: rotate(360deg);
  }
}
.editor__tokens {
  color: #6b7280;
}
.editor__steps {
  margin: 0;
  padding-left: 1.25rem;
  font-size: 0.85rem;
  color: #374151;
}
.editor__stream {
  white-space: pre-wrap;
  font-style: italic;
}
.editor__stream-label {
  font-style: normal;
  font-weight: 600;
  color: #2563eb;
}
.editor__warning {
  color: #b45309;
  margin: 0;
}
.editor__meter ul,
.editor__rag ul {
  margin: 0.25rem 0 0;
  padding-left: 1.25rem;
}
.editor__rag-source {
  font-weight: 600;
  font-family: monospace;
  color: #6b7280;
  margin-right: 0.25rem;
}
.editor__error {
  color: #b91c1c;
}
.editor__error--turn {
  border: 1px solid #fecaca;
  background: #fef2f2;
  padding: 0.5rem;
  border-radius: 0.375rem;
}
</style>
