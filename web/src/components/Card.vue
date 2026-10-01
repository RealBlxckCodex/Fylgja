<script setup lang="ts">
defineProps<{ title?: string; subtitle?: string; flush?: boolean; glow?: boolean }>()
</script>
<template>
  <section class="panel flex flex-col min-w-0" :class="{ 'panel-glow': glow }">
    <header v-if="title || $slots.actions" class="flex items-center justify-between gap-3 px-5 pt-4 pb-3">
      <div class="min-w-0">
        <h2 class="text-[13px] font-semibold tracking-wide truncate">{{ title }}</h2>
        <p v-if="subtitle" class="text-xs muted truncate mt-0.5">{{ subtitle }}</p>
      </div>
      <div class="flex items-center gap-2 shrink-0"><slot name="actions" /></div>
    </header>
    <div :class="flush ? (title || $slots.actions ? 'border-t line' : '') : 'px-5 pb-5 ' + (title || $slots.actions ? '' : 'pt-5')" class="min-w-0"><slot /></div>
  </section>
</template>
