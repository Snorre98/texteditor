<script setup lang="ts">
// ChatComposer — the window's input row (ADR-0041 §4). Enter submits, Shift+Enter
// inserts a newline. Disabled while a turn is streaming or no document is open.
import { ref, watch } from "vue";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { SendHorizontal } from "lucide-vue-next";

const props = defineProps<{
  draft: string;
  busy: boolean;
  canChat: boolean;
  placeholder?: string;
}>();

const emit = defineEmits<{
  updateDraft: [value: string];
  send: [];
}>();

const local = ref(props.draft);

watch(
  () => props.draft,
  (v) => (local.value = v),
);
watch(local, (v) => emit("updateDraft", v));

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    if (!props.busy && props.canChat && local.value.trim()) emit("send");
  }
}
</script>

<template>
  <div class="flex items-end gap-1.5 border-t p-2">
    <Textarea
      v-model="local"
      rows="2"
      :placeholder="placeholder ?? (canChat ? 'ask the engine…' : 'open a document first')"
      class="max-h-32 min-h-0 resize-none"
      :disabled="!canChat"
      @keydown="onKeydown"
    />
    <Button
      type="button"
      size="icon"
      :disabled="busy || !canChat || !local.trim()"
      aria-label="Send"
      @click="emit('send')"
    >
      <SendHorizontal />
    </Button>
  </div>
</template>
