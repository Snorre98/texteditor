<script setup lang="ts">
// MessageList — the active session's history plus the live answering stream
// (ADR-0041 §4). Messages come from the store (selectSession); the streaming
// tail is the session turn's `tokens` signal. Auto-scrolls to the newest.
import { nextTick, ref, watch } from "vue";
import { ScrollArea, ScrollBar } from "@/components/ui/scroll-area";
import { Skeleton } from "@/components/ui/skeleton";
import type { TurnState } from "../state/store";
import type { Message } from "../generated/types.gen";
import ChatMessage from "./ChatMessage.vue";

const props = defineProps<{
  sessionId: string | null;
  messages: Message[];
  turn: TurnState | undefined;
}>();

const viewport = ref<HTMLElement | null>(null);

function scrollToBottom() {
  nextTick(() => {
    const el = viewport.value?.querySelector<HTMLElement>(
      "[data-slot='scroll-area-viewport']",
    );
    if (el) el.scrollTop = el.scrollHeight;
  });
}

watch(
  () => [props.messages.length, props.turn?.tokens],
  () => scrollToBottom(),
);
</script>

<template>
  <ScrollArea ref="viewport" class="min-h-0 flex-1">
    <div class="flex flex-col gap-2 p-3">
      <p v-if="messages.length === 0 && !turn?.active" class="text-sm text-muted-foreground">
        no messages yet — ask the engine something
      </p>
      <ChatMessage
        v-for="(m, i) in messages"
        :key="`${sessionId}-${i}`"
        :role="m.role"
        :content="m.content"
      />
      <div v-if="turn?.active" class="flex w-full justify-start">
        <div class="flex max-w-[85%] items-start gap-1">
          <div class="flex min-w-0 flex-col gap-1 rounded-lg bg-muted px-3 py-2 text-sm">
            <span v-if="turn.tokens" class="whitespace-pre-wrap break-words">{{ turn.tokens }}</span>
            <Skeleton v-else class="h-4 w-24" />
            <span class="text-xs text-muted-foreground">working…</span>
          </div>
        </div>
      </div>
    </div>
    <ScrollBar orientation="vertical" />
  </ScrollArea>
</template>
