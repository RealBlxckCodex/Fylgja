<script setup lang="ts">
defineProps<{ tabs: { id: string; label: string; badge?: number | string }[]; modelValue: string }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
</script>
<template>
  <nav class="inline-flex max-w-full gap-1 p-1 rounded-xl overflow-x-auto" style="background: var(--panel-2); box-shadow: inset 0 0 0 1px var(--line)" role="tablist">
    <button v-for="t in tabs" :key="t.id" role="tab" :aria-selected="modelValue === t.id"
      class="focus-ring px-3.5 py-1.5 text-[13px] rounded-lg whitespace-nowrap transition-all flex items-center gap-1.5"
      :class="modelValue === t.id ? 'text-[var(--text)] font-medium' : 'muted hover:text-[var(--text)]'"
      :style="modelValue === t.id ? 'background: var(--panel-solid); box-shadow: 0 1px 0 rgba(255,255,255,.07) inset, 0 4px 12px -4px rgba(0,0,0,.6)' : ''"
      @click="emit('update:modelValue', t.id)">
      {{ t.label }}<span v-if="t.badge" class="mono text-[10px] rounded-full bg-accent text-white px-1.5 py-px">{{ t.badge }}</span>
    </button>
  </nav>
</template>
