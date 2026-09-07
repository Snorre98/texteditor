<script setup lang="ts">
// ChatMessage — one message bubble (ADR-0041 §4). Assistant messages render
// sanitized markdown (chatMarkdown.ts); user messages are plain text. A copy
// action sits beside the assistant content. Dumb rendering only — content
// comes from the store via the session's message list.
import { ref } from "vue";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Check, Copy } from "lucide-vue-next";
import { renderMarkdown } from "./chatMarkdown";

const props = defineProps<{
  role: "user" | "assistant" | string;
  content: string;
}>();

const copied = ref(false);

async function copyContent() {
  try {
    await navigator.clipboard.writeText(props.content);
    copied.value = true;
    setTimeout(() => (copied.value = false), 1500);
  } catch {
    // clipboard can be unavailable in some WebView contexts — non-fatal
  }
}
</script>

<template>
  <div class="flex w-full" :class="role === 'user' ? 'justify-end' : 'justify-start'">
    <div class="flex max-w-[85%] items-start gap-1" :class="role === 'user' ? 'flex-row-reverse' : ''">
      <Bubble :variant="role === 'user' ? 'secondary' : 'muted'" :align="role === 'user' ? 'end' : 'start'">
        <BubbleContent>
          <div v-if="role === 'user'" class="whitespace-pre-wrap break-words">{{ content }}</div>
          <div
            v-else
            class="chat-markdown min-w-0 break-words"
            v-html="renderMarkdown(content)"
          />
        </BubbleContent>
      </Bubble>
      <TooltipProvider v-if="role === 'assistant'">
        <Tooltip>
          <TooltipTrigger as-child>
            <Button variant="ghost" size="icon-xs" class="text-muted-foreground" @click="copyContent">
              <Check v-if="copied" />
              <Copy v-else />
            </Button>
          </TooltipTrigger>
          <TooltipContent>Copy</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
  </div>
</template>

<style>
/* Scoped markdown typography inside assistant bubbles (chat-markdown.ts
 * sanitizes; these rules only style, never execute). */
.chat-markdown :deep(p) {
  margin: 0 0 0.4rem;
}
.chat-markdown :deep(p:last-child) {
  margin-bottom: 0;
}
.chat-markdown :deep(ul),
.chat-markdown :deep(ol) {
  margin: 0.2rem 0 0.4rem;
  padding-left: 1.2rem;
}
.chat-markdown :deep(pre) {
  margin: 0.3rem 0;
  padding: 0.4rem 0.5rem;
  border-radius: 0.3rem;
  background: hsl(var(--muted) / 0.6);
  overflow-x: auto;
  font-size: 0.85em;
}
.chat-markdown :deep(code) {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.9em;
}
.chat-markdown :deep(p code) {
  padding: 0.1rem 0.25rem;
  border-radius: 0.25rem;
  background: hsl(var(--muted) / 0.6);
}
</style>
