<script setup lang="ts">
// MeterPanel — the per-turn token tally (ADR-0013 §1; ADR-0041 §4), collapsible.
// The pedagogical core: every component of the context is a visible line item.
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { ChevronDown } from "lucide-vue-next";
import type { MeterTally } from "../state/store";
import { METER_COMPONENTS, type MeterComponent } from "../state/store";

defineProps<{
  cumulative: MeterTally | undefined;
}>();
</script>

<template>
  <Collapsible
    v-if="cumulative && METER_COMPONENTS.some((c) => (cumulative as Record<MeterComponent, number>)[c] > 0)"
    class="border-t px-2"
  >
    <CollapsibleTrigger class="flex w-full items-center gap-1 py-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
      <ChevronDown class="size-3 transition-transform data-[state=open]:rotate-180" />
      meter
    </CollapsibleTrigger>
    <CollapsibleContent>
      <ul class="mb-1.5 flex flex-col gap-0.5 text-sm">
        <li
          v-for="c in METER_COMPONENTS"
          :key="c"
          class="flex justify-between gap-2"
        >
          <span class="text-muted-foreground">{{ c }}</span>
          <span class="font-mono">{{ (cumulative as Record<MeterComponent, number>)[c] }}</span>
        </li>
      </ul>
    </CollapsibleContent>
  </Collapsible>
</template>
