<script setup lang="ts">
// CandidateMerge — the side-by-side candidate diff (ADR-0013 §2, ADR-0026). When
// a candidate event names a block, the block's current text is shown left and the
// staged candidate right via @codemirror/merge, with Accept/Reject controls. All
// accept/apply/commit happens through the store (ADR-0013 §3); this component
// only renders the diff and routes the user's decision.
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { MergeView } from "@codemirror/merge";
import { markdown } from "@codemirror/lang-markdown";
import type { AppStore } from "../state/store";

const props = defineProps<{
  store: AppStore;
  blockId: string;
  /** The block's current (base) text. */
  baseText: string;
}>();

const emit = defineEmits<{
  accept: [blockId: string];
  reject: [];
}>();

const container = ref<HTMLElement | null>(null);
let merge: MergeView | null = null;
const candidateText = ref<string | null>(null);
const busy = ref(false);

async function loadCandidate() {
  candidateText.value = await props.store.getCandidateText(props.blockId);
  if (candidateText.value === null) emit("reject");
}

async function accept() {
  busy.value = true;
  try {
    await props.store.acceptCandidate(props.blockId);
    emit("accept", props.blockId);
  } finally {
    busy.value = false;
  }
}

onMounted(async () => {
  await loadCandidate();
  if (!container.value || candidateText.value === null) return;

  merge = new MergeView({
    a: {
      doc: props.baseText,
      extensions: [EditorState.readOnly.of(true), markdown(), EditorView.lineWrapping],
    },
    b: {
      doc: candidateText.value,
      extensions: [EditorState.readOnly.of(true), markdown(), EditorView.lineWrapping],
    },
    parent: container.value,
    revertControls: "a-to-b",
    highlightChanges: true,
  });
});

watch(
  () => props.blockId,
  () => loadCandidate(),
);

onBeforeUnmount(() => {
  merge?.destroy();
});
</script>

<template>
  <div class="candidate-merge">
    <div class="candidate-merge__controls">
      <span class="candidate-merge__label">AI proposal</span>
      <button type="button" :disabled="busy || candidateText === null" @click="accept">
        Accept
      </button>
      <button type="button" @click="emit('reject')">Reject</button>
    </div>
    <div ref="container" class="candidate-merge__view" />
  </div>
</template>

<style scoped>
.candidate-merge {
  border: 1px solid #e5e7eb;
  border-radius: 0.375rem;
  overflow: hidden;
}
.candidate-merge__controls {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.4rem 0.6rem;
  background: #f9fafb;
  border-bottom: 1px solid #e5e7eb;
  font-size: 0.85rem;
}
.candidate-merge__label {
  font-weight: 600;
  margin-right: auto;
}
.candidate-merge__view {
  height: 16rem;
  overflow: auto;
}
</style>
