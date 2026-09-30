<script setup lang="ts">
import { computed } from 'vue'
const props = defineProps<{ tone?: 'ok' | 'warn' | 'err' | 'info' | 'muted' | 'accent'; icon?: string }>()
const color = computed(() => ({ ok: 'var(--ok)', warn: 'var(--warn)', err: 'var(--err)', info: 'var(--info)', muted: 'var(--muted)', accent: 'var(--accent-2)' })[props.tone ?? 'muted'])
</script>
<template>
  <span class="inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-[11px] font-medium border whitespace-nowrap"
    :style="{ color, borderColor: color + '55', background: color + '14' }">
    <span v-if="icon" aria-hidden="true">{{ icon }}</span><slot />
  </span>
</template>
