<script setup lang="ts">
defineProps<{ tabs: { id: string; label: string; badge?: number | string }[]; modelValue: string }>()
const emit = defineEmits<{ 'update:modelValue': [string] }>()
</script>
<template>
  <nav class="flex gap-1 border-b line overflow-x-auto" role="tablist">
    <button v-for="t in tabs" :key="t.id" role="tab" :aria-selected="modelValue === t.id"
      class="focus-ring px-3 py-2 text-sm -mb-px border-b-2 whitespace-nowrap"
      :class="modelValue === t.id ? 'border-[var(--accent)] text-[var(--text)]' : 'border-transparent muted hover:text-[var(--text)]'"
      @click="emit('update:modelValue', t.id)">
      {{ t.label }}<span v-if="t.badge" class="ml-1.5 mono text-[11px] rounded bg-accent text-white px-1">{{ t.badge }}</span>
    </button>
  </nav>
</template>
