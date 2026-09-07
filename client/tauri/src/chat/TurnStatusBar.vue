<script setup lang="ts">
// TurnStatusBar — the active turn's lifecycle line (ADR-0041 §4): status,
// phase steps transcript, backpressure warning, and the retry-last action.
// Working/done/error status comes from useAssistant.turnStatus.
import { computed } from "vue";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { RotateCcw } from "lucide-vue-next";
import { errorDisplay, totalTokens, turnStatus, turnSummary } from "../editor/useAssistant";
import { stepLabel, type TurnState } from "../state/store";

const props = defineProps<{
  turn: TurnState | undefined;
  canRetry: boolean;
}>();

const emit = defineEmits<{ retry: [] }>();

const status = computed(() => turnStatus(props.turn));
</script>

<template>
  <div v-if="turn && status !== 'idle'" class="flex flex-col gap-1 border-t px-3 py-1.5 text-xs">
    <div class="flex items-center gap-2">
      <Badge
        :variant="status === 'error' ? 'destructive' : status === 'working' ? 'secondary' : 'outline'"
      >
        <span v-if="status === 'working'" class="size-1.5 rounded-full bg-current animate-pulse mr-1" />
        {{ status === "working" ? "working…" : status === "error" ? "failed" : `done — ${turnSummary(turn.done)}` }}
      </Badge>
      <span v-if="status === 'done' && totalTokens(turn.cumulative) > 0" class="text-muted-foreground">
        {{ totalTokens(turn.cumulative) }} tokens
      </span>
      <Button
        v-if="canRetry && status !== 'working'"
        variant="ghost"
        size="xs"
        class="ml-auto text-muted-foreground"
        @click="emit('retry')"
      >
        <RotateCcw /> retry
      </Button>
    </div>
    <ol v-if="turn.steps?.length" class="flex flex-col gap-0.5 text-muted-foreground">
      <li v-for="(s, i) in turn.steps" :key="i">{{ stepLabel(s) }}</li>
    </ol>
    <p v-if="turn.backpressure" class="text-amber-600 dark:text-amber-400">
      client buffer overflow — some events were dropped
    </p>
    <p v-if="turn.error" class="text-destructive">{{ errorDisplay(turn.error) }}</p>
  </div>
</template>
