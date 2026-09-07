<script setup lang="ts">
// SessionList — the document's sessions, resumable with full history
// (ADR-0041 §3). Labels are derived (sessionLabels.ts); the API has no titles.
// A session with an active turn shows a working dot (concurrency, ADR-0026 §4).
import { computed } from "vue";
import { Button } from "@/components/ui/button";
import { Plus } from "lucide-vue-next";
import type { AppStore } from "../state/store";
import { sessionLabel, sessionSubLabel, sessionWorking, sortSessions } from "./sessionLabels";

const props = defineProps<{
  store: AppStore;
  activeSessionId: string | null;
}>();

const emit = defineEmits<{
  select: [id: string];
  newChat: [];
}>();

const sessions = computed(() => sortSessions(props.store.state.sessions));
</script>

<template>
  <div class="flex h-full flex-col">
    <div class="flex items-center justify-between gap-1 px-2 py-1.5">
      <span class="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        sessions
      </span>
      <Button variant="ghost" size="icon-xs" @click="emit('newChat')" aria-label="New chat">
        <Plus />
      </Button>
    </div>
    <div class="min-h-0 flex-1 overflow-y-auto px-1.5 pb-1.5">
      <p v-if="sessions.length === 0" class="px-2 py-1 text-xs text-muted-foreground">
        no sessions for this document yet
      </p>
      <ul v-else class="flex flex-col gap-0.5">
        <li v-for="s in sessions" :key="s.id">
          <button
            type="button"
            class="flex w-full flex-col rounded-md px-2 py-1.5 text-left transition-colors hover:bg-muted"
            :class="s.id === activeSessionId ? 'bg-muted' : ''"
            @click="emit('select', s.id)"
          >
            <span class="flex items-center gap-1.5 text-sm font-medium">
              <span
                v-if="sessionWorking(store.state.sessionStates, s.id)"
                class="size-1.5 shrink-0 rounded-full bg-blue-500 animate-pulse"
                aria-label="working"
              />
              <span v-else class="size-1.5 shrink-0 rounded-full bg-transparent" />
              {{ sessionLabel(s) }}
            </span>
            <span class="text-xs text-muted-foreground">{{ sessionSubLabel(s) }}</span>
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>
