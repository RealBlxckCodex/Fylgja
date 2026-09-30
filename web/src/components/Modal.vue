<script setup lang="ts">
defineProps<{ open: boolean; title: string; wide?: boolean }>()
const emit = defineEmits<{ close: [] }>()
</script>
<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60" @click.self="emit('close')" @keydown.esc="emit('close')">
      <div class="panel w-full max-h-[90vh] overflow-auto" :class="wide ? 'max-w-4xl' : 'max-w-lg'" role="dialog" aria-modal="true" :aria-label="title">
        <header class="flex items-center justify-between px-5 py-3 border-b line">
          <h2 class="font-semibold">{{ title }}</h2>
          <button class="muted hover:text-[var(--text)] focus-ring rounded px-1" aria-label="Schließen" @click="emit('close')">✕</button>
        </header>
        <div class="p-5"><slot /></div>
      </div>
    </div>
  </Teleport>
</template>
