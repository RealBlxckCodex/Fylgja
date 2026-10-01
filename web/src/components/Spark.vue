<script setup lang="ts">
// Mini-Zeitreihe als SVG-Fläche.
import { computed } from 'vue'
const props = defineProps<{ data: number[]; w?: number; h?: number; color?: string }>()
const W = computed(() => props.w ?? 120), H = computed(() => props.h ?? 36)
const pts = computed(() => {
  const d = props.data.length > 1 ? props.data : [0, 0]
  const max = Math.max(...d, 1e-9), min = Math.min(...d, 0)
  return d.map((v, i) => [(i / (d.length - 1)) * W.value, H.value - 3 - ((v - min) / (max - min || 1)) * (H.value - 6)])
})
const line = computed(() => pts.value.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' '))
const area = computed(() => line.value + ` L${W.value} ${H.value} L0 ${H.value} Z`)
const id = 'sp' + Math.random().toString(36).slice(2, 7)
</script>
<template>
  <svg :width="W" :height="H" :viewBox="`0 0 ${W} ${H}`" aria-hidden="true" class="overflow-visible">
    <defs><linearGradient :id="id" x1="0" y1="0" x2="0" y2="1"><stop offset="0" :stop-color="color ?? 'var(--accent-2)'" stop-opacity=".35"/><stop offset="1" :stop-color="color ?? 'var(--accent-2)'" stop-opacity="0"/></linearGradient></defs>
    <path :d="area" :fill="`url(#${id})`" />
    <path :d="line" fill="none" :stroke="color ?? 'var(--accent-2)'" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round" />
  </svg>
</template>
