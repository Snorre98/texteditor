<script setup lang="ts">
// Editor — the CodeMirror 6 editor (F8, ADR-0013 §2). Renders the document's
// block tree as markdown, offers a selection-anchored chat bubble (CodeMirror
// tooltip API, ADR-0026), side-by-side candidates (@codemirror/merge), and the
// manual-edit autosave cadence (ADR-0020 §1, ADR-0038). It is a dumb client:
// every edit, session, and versioning action is routed through the store to the
// engine (ADR-0013 §3).
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { EditorState, StateEffect, StateField } from "@codemirror/state";
import { EditorView, ViewPlugin, showTooltip, type ViewUpdate } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { markdown } from "@codemirror/lang-markdown";
import type { AppStore } from "../state/store";
import { capabilityAdapter } from "../capability";
import { blockIndexAt, blockRanges, blockTreeToMarkdown, markdownToBlockWrites } from "./blocks";
import { createAutosave, DEFAULT_AUTOSAVE_INTERVAL_MS } from "./autosave";
import CandidateMerge from "./CandidateMerge.vue";

const props = defineProps<{ store: AppStore }>();

const container = ref<HTMLElement | null>(null);
const error = ref<string | null>(null);
const activeSessionId = ref<string | null>(null);

let view: EditorView | null = null;
let autosave: ReturnType<typeof createAutosave> | null = null;

const defaultModeName = () => props.store.state.modes[0]?.name ?? "proofreader";

// --------------------------- selection bubble (ADR-0026 §1) ---------------------------

interface BubbleAnchor {
  pos: number;
  end: number;
}

const setBubble = StateEffect.define<BubbleAnchor | null>();

const bubbleField = StateField.define<BubbleAnchor | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setBubble)) value = e.value;
    return value;
  },
  provide: (f) =>
    showTooltip.from(f, (anchor) => {
      if (!anchor) return null;
      return {
        pos: anchor.end,
        above: true,
        arrow: true,
        create: () => {
          const dom = document.createElement("div");
          dom.className = "texteditor-bubble";
          const btn = document.createElement("button");
          btn.type = "button";
          btn.textContent = "Ask about selection";
          btn.addEventListener("click", () => void askAbout(anchor));
          dom.appendChild(btn);
          return { dom };
        },
      };
    }),
});

const bubblePlugin = ViewPlugin.fromClass(
  class {
    constructor(v: EditorView) {
      updateBubble(v);
    }
    update(u: ViewUpdate) {
      if (u.selectionSet || u.docChanged) updateBubble(u.view);
    }
  },
);

function updateBubble(v: EditorView) {
  const sel = v.state.selection.main;
  const has = !sel.empty;
  v.dispatch({ effects: setBubble.of(has ? { pos: sel.from, end: sel.to } : null) });
}

// askAbout creates-or-resumes a session anchored to the selected block and runs
// a turn (ADR-0026 §1–§3). Re-selecting the same block reopens its session.
async function askAbout(anchor: BubbleAnchor) {
  const store = props.store;
  if (!store.state.document || !view) return;
  const docText = view.state.doc.toString();
  const idx = blockIndexAt(blockRanges(docText), anchor.pos);
  const blockId = idx >= 0 ? store.state.blocks[idx]?.id : undefined;
  const selected = docText.slice(anchor.pos, anchor.end);

  try {
    const sessionId = await store.createSession(blockId, undefined);
    activeSessionId.value = sessionId;
    await store.submitTurn({
      sessionId,
      modeName: defaultModeName(),
      documentId: store.state.document.id,
      userInput: selected
        ? `Improve this selection:\n\n${selected}`
        : "Improve this selection",
    });
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

// --------------------------- document load + autosave ---------------------------

async function openFile() {
  try {
    const picked = await capabilityAdapter().pickFile();
    if (!picked?.path) return;
    await props.store.openDocument(picked.path);
    replaceDocument(blockTreeToMarkdown(props.store.state.blocks));
    activeSessionId.value = null;
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

function replaceDocument(md: string) {
  view?.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: md } });
}

function doSave() {
  if (!props.store.state.document || !view) return;
  const tree = markdownToBlockWrites(view.state.doc.toString(), props.store.state.blocks);
  void props.store.saveTree(tree);
}

// --------------------------- derived state ---------------------------

const activeSession = computed(() =>
  activeSessionId.value ? props.store.state.sessionStates[activeSessionId.value] : undefined,
);

const activeTurn = computed(() => activeSession.value?.turn);

const candidateBlockId = computed(() => {
  const cand = activeTurn.value?.candidate;
  return cand && cand.ok ? cand.blockId : undefined;
});

const candidateBaseText = computed(() => {
  const id = candidateBlockId.value;
  if (!id) return "";
  return props.store.state.blocks.find((b) => b.id === id)?.text ?? "";
});

const messages = computed(() => activeSession.value?.messages ?? []);

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
        bubbleField,
        bubblePlugin,
        EditorView.updateListener.of((u) => {
          if (u.docChanged) autosave?.noteEdit();
        }),
      ],
    }),
  });

  autosave = createAutosave({
    intervalMs: DEFAULT_AUTOSAVE_INTERVAL_MS,
    onSave: doSave,
  });
});

onBeforeUnmount(() => {
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

    <section v-if="activeSessionId" class="editor__chat">
      <h2>Chat</h2>
      <ol class="editor__messages">
        <li v-for="(m, i) in messages" :key="i" :class="`role-${m.role}`">
          <strong>{{ m.role }}:</strong> {{ m.content }}
        </li>
      </ol>
      <p v-if="activeTurn?.tokens" class="editor__stream">{{ activeTurn.tokens }}</p>
      <p v-if="activeTurn?.error" class="editor__error">
        {{ activeTurn.error.code }}: {{ activeTurn.error.message }}
      </p>
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
}
.editor__messages {
  margin: 0;
  padding-left: 1.25rem;
}
.editor__stream {
  white-space: pre-wrap;
  font-style: italic;
}
.editor__error {
  color: #b91c1c;
}
</style>

<style>
.texteditor-bubble {
  background: #111827;
  color: #fff;
  border-radius: 0.375rem;
  padding: 0.25rem;
  font-size: 0.85rem;
}
.texteditor-bubble button {
  background: #2563eb;
  color: #fff;
  border: 0;
  border-radius: 0.25rem;
  padding: 0.3rem 0.6rem;
  cursor: pointer;
}
</style>
