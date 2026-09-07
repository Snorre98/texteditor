<script setup lang="ts">
// ModelSelector — the chat window's model select with serving observability
// (ADR-0040, ADR-0041 §4). Selecting a model drives the lifecycle write side:
// start new → up → stop old (store.switchModel; ADR-0007 verbs). liveState is
// rendered from the /fleet slice; a control-plane outage is a labeled banner,
// never a silent freeze.
import { computed } from "vue";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { RefreshCw } from "lucide-vue-next";
import type { AppStore } from "../state/store";

const props = defineProps<{ store: AppStore }>();

const models = computed(() => props.store.state.fleet.models);
const busy = computed(() => props.store.state.fleet.busy);
const control = computed(() => props.store.state.fleet.control);

const currentUp = computed(
  () => models.value.find((m) => m.liveState === "up")?.name ?? "",
);

function onSelect(value: unknown) {
  if (typeof value !== "string") return;
  if (value === currentUp.value) return;
  void props.store.switchModel(currentUp.value, value);
}

const liveStateTone: Record<string, string> = {
  up: "text-emerald-600 dark:text-emerald-400",
  down: "text-muted-foreground",
  starting: "text-blue-600 dark:text-blue-400",
  stopping: "text-amber-600 dark:text-amber-400",
  provisioning: "text-purple-600 dark:text-purple-400",
  unknown: "text-muted-foreground",
};
</script>

<template>
  <div class="flex min-w-0 flex-col">
    <div class="flex items-center gap-1">
      <Select :model-value="currentUp" @update:model-value="onSelect">
        <SelectTrigger class="h-7 min-w-0 flex-1" aria-label="Model">
          <SelectValue :placeholder="currentUp || 'select model…'" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem v-for="m in models" :key="m.name" :value="m.name">
            <span class="flex items-center gap-1.5">
              {{ m.name }}
              <span class="text-xs" :class="liveStateTone[m.liveState] ?? ''">· {{ m.liveState }}</span>
            </span>
          </SelectItem>
        </SelectContent>
      </Select>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label="Refresh fleet"
        @click="() => props.store.refreshFleet()"
      >
        <RefreshCw :class="busy ? 'animate-spin' : ''" />
      </Button>
    </div>
    <p v-if="busy" class="text-xs text-muted-foreground">{{ busy }} — serving command in flight…</p>
    <p v-else-if="control === 'unreachable'" class="text-xs text-amber-600 dark:text-amber-400">
      serving control unavailable — showing last known models
    </p>
  </div>
</template>
