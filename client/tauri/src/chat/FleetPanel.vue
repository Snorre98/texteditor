<script setup lang="ts">
// FleetPanel — the serving observability surface (ADR-0040 §5): every model's
// liveState plus the remediation verbs (start/stop), a manual refresh, and the
// control-plane banner. A faithful port of the original `editor__fleet` panel
// from the static chat block (commit 81b7c96) into the floating chat window
// (ADR-0041 §4: the full feature surface moves into the window). Dumb client:
// all effects route through the store's fleet actions.
import { computed } from "vue";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { ChevronDown, RefreshCw } from "lucide-vue-next";
import type { AppStore } from "../state/store";

const props = defineProps<{
  store: AppStore;
  /** The currently selected mode — its defaultModel is "serving" when up. */
  modeName: string;
}>();

// Which model currently serves the selected mode — its defaultModel when that
// is up, else the first up model (ADR-0040 §5, verbatim semantics).
const currentModel = computed(() => {
  const fleet = props.store.state.fleet;
  const mode = props.store.state.modes.find((m) => m.name === props.modeName);
  const def = mode?.defaultModel;
  const isUp = (name: string) =>
    fleet.models.find((m) => m.name === name)?.liveState === "up";
  if (def && isUp(def)) return def;
  return fleet.models.find((m) => m.liveState === "up")?.name ?? "";
});

const busy = computed(() => props.store.state.fleet.busy);
</script>

<template>
  <Collapsible
    v-if="store.state.fleet.models.length > 0"
    class="border-t px-2"
    :default-open="true"
  >
    <CollapsibleTrigger class="flex w-full items-center gap-1 py-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
      <ChevronDown class="size-3 transition-transform data-[state=open]:rotate-180" />
      models
      <span
        class="ml-auto inline-flex items-center gap-1 normal-case font-normal"
      >
        <span class="text-muted-foreground">{{ currentModel || "none up" }}</span>
      </span>
    </CollapsibleTrigger>
    <CollapsibleContent>
      <div class="mb-1.5 flex flex-col gap-1">
        <div class="flex items-center justify-between gap-2">
          <p
            v-if="store.state.fleet.control === 'unreachable'"
            class="text-xs text-amber-600 dark:text-amber-400"
          >
            serving control unavailable — showing last known models
          </p>
          <button
            type="button"
            class="ml-auto flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-muted disabled:opacity-50"
            :disabled="busy !== null"
            @click="store.refreshFleet()"
          >
            <RefreshCw class="size-3" :class="busy ? 'animate-spin' : ''" />
            check servers
          </button>
        </div>
        <ul class="flex flex-col">
          <li
            v-for="m in store.state.fleet.models"
            :key="m.name"
            class="flex items-center gap-1.5 py-0.5 text-sm"
          >
            <span class="size-1.5 shrink-0 rounded-full" :class="`dot-${m.liveState}`" />
            <span class="font-mono">{{ m.name }}</span>
            <span v-if="currentModel === m.name" class="text-xs font-semibold text-emerald-600 dark:text-emerald-400">
              serving
            </span>
            <span class="hidden overflow-hidden font-mono text-xs text-muted-foreground text-ellipsis whitespace-nowrap sm:inline">
              {{ m.baseUrl }}
            </span>
            <span class="ml-auto text-xs">
              <span v-if="m.liveState === 'starting' || m.liveState === 'provisioning'">
                {{ m.liveState }}…
              </span>
              <button
                v-else-if="m.liveState === 'up' && currentModel !== m.name"
                type="button"
                class="rounded-md border px-1.5 py-0.5 text-muted-foreground hover:bg-muted disabled:opacity-50"
                :disabled="busy !== null"
                @click="store.stopModel(m.name)"
              >
                {{ busy === m.name ? "stopping…" : "stop" }}
              </button>
              <button
                v-else-if="m.liveState === 'down' || m.liveState === 'unknown'"
                type="button"
                class="rounded-md border px-1.5 py-0.5 text-muted-foreground hover:bg-muted disabled:opacity-50"
                :disabled="busy !== null"
                @click="store.startModel(m.name)"
              >
                {{ busy === m.name ? "starting…" : "start" }}
              </button>
            </span>
          </li>
        </ul>
        <p v-if="store.state.fleet.error" class="text-xs text-destructive">
          {{ store.state.fleet.error }}
        </p>
      </div>
    </CollapsibleContent>
  </Collapsible>
</template>

<style scoped>
.dot-up {
  background: #047857;
}
.dot-down {
  background: #b91c1c;
}
.dot-starting,
.dot-provisioning {
  background: #b45309;
  animation: fleet-pulse 1s ease-in-out infinite;
}
.dot-unknown {
  background: #9ca3af;
}
@keyframes fleet-pulse {
  50% {
    opacity: 0.35;
  }
}
</style>
