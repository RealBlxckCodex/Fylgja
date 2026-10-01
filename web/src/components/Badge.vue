<script setup lang="ts">
import { computed } from 'vue'
const props = defineProps<{ tone?: 'ok' | 'warn' | 'err' | 'info' | 'muted' | 'accent'; icon?: string }>()
const color = computed(() => ({ ok: 'var(--ok)', warn: 'var(--warn)', err: 'var(--err)', info: 'var(--info)', muted: 'var(--muted)', accent: 'var(--accent-2)' })[props.tone ?? 'muted'])
</script>
<template>
  <span class="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium whitespace-nowrap"
    :style="{ color, background: `color-mix(in srgb, ${color} 13%, transparent)`, boxShadow: `inset 0 0 0 1px color-mix(in srgb, ${color} 28%, transparent)` }">
    <span v-if="icon" aria-hidden="true" class="text-[10px]">{{ icon }}</span><slot />
  </span>
</template>
