<script setup lang="ts">
// RagPanel — the latest retrieval result for the active turn (ADR-0041 §4),
// collapsible, showing source provenance per chunk.
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { ChevronDown } from "lucide-vue-next";
import type { RagEvent } from "../generated/types.gen";

defineProps<{
  rag: RagEvent | null | undefined;
}>();
</script>

<template>
  <Collapsible v-if="rag && rag.chunks && rag.chunks.length > 0" class="border-t px-2">
    <CollapsibleTrigger class="flex w-full items-center gap-1 py-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
      <ChevronDown class="size-3 transition-transform data-[state=open]:rotate-180" />
      retrieval · {{ rag.chunks.length }} chunk{{ rag.chunks.length === 1 ? "" : "s" }}
    </CollapsibleTrigger>
    <CollapsibleContent>
      <ul class="mb-1.5 flex flex-col gap-1 text-sm">
        <li v-for="(c, i) in rag.chunks" :key="i" class="rounded-md bg-muted/60 p-1.5">
          <span class="font-mono text-xs text-muted-foreground">{{ c.source ?? c.blockId }}</span>
          <p class="whitespace-pre-wrap">{{ c.text }}</p>
        </li>
      </ul>
    </CollapsibleContent>
  </Collapsible>
</template>
