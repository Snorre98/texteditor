<script setup lang="ts">
// ChatOrb — the minimized chat window's floating restore button (ADR-0041 §2).
import { Button } from "@/components/ui/button";
import { MessagesSquare } from "lucide-vue-next";

const emit = defineEmits<{ restore: [] }>();
</script>

<template>
  <Button
    class="fixed bottom-4 right-4 z-50 size-11 rounded-full shadow-lg"
    aria-label="Restore chat"
    @click="emit('restore')"
  >
    <MessagesSquare />
  </Button>
</template>
