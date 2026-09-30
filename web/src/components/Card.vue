<script setup lang="ts">
defineProps<{ title?: string; subtitle?: string; flush?: boolean }>()
</script>
<template>
  <section class="panel flex flex-col min-w-0">
    <header v-if="title || $slots.actions" class="flex items-center justify-between gap-3 px-4 py-3 border-b line">
      <div class="min-w-0">
        <h2 class="text-sm font-semibold truncate">{{ title }}</h2>
        <p v-if="subtitle" class="text-xs muted truncate">{{ subtitle }}</p>
      </div>
      <div class="flex items-center gap-2 shrink-0"><slot name="actions" /></div>
    </header>
    <div :class="flush ? '' : 'p-4'" class="min-w-0"><slot /></div>
  </section>
</template>
