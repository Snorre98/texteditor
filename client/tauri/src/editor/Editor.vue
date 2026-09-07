<script setup lang="ts">
// Editor — the CodeMirror 6 editor (F8, ADR-0013 §2). Renders the document's
// block tree as markdown, offers a selection-anchored "Ask about selection"
// toolbar button (ADR-0026 §1, deterministic, mouse + keyboard), side-by-side
// candidates (@codemirror/merge), and the manual-edit autosave cadence
// (ADR-0020 §1, ADR-0038). The chat itself moved to the floating window
// (ADR-0041): this component now owns only the workspace — toolbar, editor
// surface, candidate preview — and shares the assistant with the window.
// It is a dumb client: every edit, session, and versioning action is routed
// through the store to the engine (ADR-0013 §3).
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { markdown } from "@codemirror/lang-markdown";
import type { AppStore } from "../state/store";
import type { createAssistant, EditorApi } from "./useAssistant";
import { errorDisplay, hasSelectionFor } from "./useAssistant";
import { capabilityAdapter } from "../capability";
import { isTauri } from "../engine";
import { blockTreeToMarkdown, markdownToBlockWrites } from "./blocks";
import { createAutosave, DEFAULT_AUTOSAVE_INTERVAL_MS, isSaveShortcut, saveStatusLabel } from "./autosave";
import CandidateMerge from "./CandidateMerge.vue";

const props = defineProps<{
  store: AppStore;
  assistant: ReturnType<typeof createAssistant>;
  editorApi: { current: EditorApi | null };
}>();

const emit = defineEmits<{ toggleChat: [] }>();

const container = ref<HTMLElement | null>(null);
const error = ref<string | null>(null);
const hasSelection = ref(false);
const dirty = ref(false);
const savedAt = ref<Date | null>(null);

const assistant = props.assistant;
const { busy, activeTurn, askAbout } = assistant;
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
    // Session loading for the floating window is driven by its document watch
    // (ADR-0041 §3); nothing to do here.
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

  // Register the document/selection readers the shared assistant delegates
  // through (ADR-0041 §3); unregistered on teardown.
  props.editorApi.current = {
    getDocText: () => view?.state.doc.toString() ?? "",
    getSelection: () =>
      view
        ? { from: view.state.selection.main.from, to: view.state.selection.main.to }
        : null,
  };

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
  props.editorApi.current = null;
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
        @click="() => void askAbout()"
      >
        Ask about selection
      </button>
      <button type="button" :disabled="!store.state.document" @click="saveNow">
        Save
      </button>
      <button type="button" class="editor__chat-toggle" @click="emit('toggleChat')">
        Chat
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

    <p v-if="assistantError" class="editor__error">
      {{ errorDisplay({ message: assistantError ?? undefined }) }}
    </p>
  </div>
</template>

<style scoped>
.editor {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  height: 100%;
  width: 100%;
  max-width: 60rem;
  margin: 0 auto;
  padding: 0.75rem;
}
.editor__toolbar {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.editor__toolbar button {
  padding: 0.3rem 0.7rem;
  font-size: 0.9rem;
  border: 1px solid #d1d5db;
  border-radius: 0.375rem;
  background: transparent;
  cursor: pointer;
}
.editor__toolbar button:disabled {
  opacity: 0.5;
  cursor: default;
}
.editor__chat-toggle {
  margin-left: auto;
}
.editor__path {
  font-size: 0.85rem;
  color: #6b7280;
  font-family: monospace;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 40%;
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
  flex: 1;
  min-height: 0;
  overflow: hidden;
}
.editor__view :deep(.cm-editor) {
  height: 100%;
  overflow-y: auto;
}
.editor__view :deep(.cm-scroller) {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.editor__error {
  color: #b91c1c;
}
</style>
