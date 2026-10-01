<script setup lang="ts">
defineProps<{ open: boolean; title: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()
</script>
<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/65 backdrop-blur-sm" @click.self="emit('close')" @keydown.esc="emit('close')">
      <div class="panel w-full max-h-[90vh] overflow-auto rise" :class="wide ? 'max-w-4xl' : 'max-w-lg'" style="background: var(--panel-solid)" role="dialog" aria-modal="true" :aria-label="title">
        <header class="flex items-center justify-between px-6 pt-5 pb-3"><h2 class="font-semibold text-base">{{ title }}</h2>
          <button class="muted hover:text-[var(--text)] focus-ring rounded-lg p-1" aria-label="Schließen" @click="emit('close')">✕</button></header>
        <div class="px-6 pb-6"><slot /></div>
      </div>
    </div>
  </Teleport>
</template>
