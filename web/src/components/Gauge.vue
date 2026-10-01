<script setup lang="ts">
// Ringanzeige 0..1. Farbe wechselt bei hoher Last (warn > .8, err > .95).
import { computed } from 'vue'
const props = defineProps<{ value: number; size?: number; label?: string; sub?: string; invert?: boolean }>()
const s = computed(() => props.size ?? 84)
const r = computed(() => s.value / 2 - 7)
const c = computed(() => 2 * Math.PI * r.value)
const v = computed(() => Math.min(1, Math.max(0, props.value || 0)))
const color = computed(() => (props.invert ? 'var(--accent-2)' : v.value > 0.95 ? 'var(--err)' : v.value > 0.8 ? 'var(--warn)' : 'var(--accent-2)'))
</script>
<template>
  <div class="relative inline-flex items-center justify-center" :style="{ width: s + 'px', height: s + 'px' }" role="img" :aria-label="`${label ?? ''} ${Math.round(v * 100)} Prozent`">
    <svg :width="s" :height="s" class="-rotate-90">
      <circle :cx="s / 2" :cy="s / 2" :r="r" fill="none" stroke="var(--line)" stroke-width="6" />
      <circle :cx="s / 2" :cy="s / 2" :r="r" fill="none" :stroke="color" stroke-width="6" stroke-linecap="round" :stroke-dasharray="c" :stroke-dashoffset="c * (1 - v)" style="transition: stroke-dashoffset .6s cubic-bezier(.2,.7,.2,1), stroke .3s; filter: drop-shadow(0 0 4px var(--accent-glow))" />
    </svg>
    <div class="absolute text-center leading-tight">
      <div class="mono font-semibold" :style="{ fontSize: s / 4.2 + 'px' }">{{ Math.round(v * 100) }}<span class="muted" :style="{ fontSize: s / 7 + 'px' }">%</span></div>
      <div v-if="label" class="muted" :style="{ fontSize: Math.max(9, s / 9) + 'px' }">{{ label }}</div>
    </div>
  </div>
</template>
