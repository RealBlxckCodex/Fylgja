<script setup lang="ts">
import Spark from './Spark.vue'
defineProps<{ label: string; value: string; hint?: string; spark?: number[]; tone?: 'ok' | 'warn' | 'err' | 'accent'; to?: string }>()
</script>
<template>
  <component :is="to ? 'RouterLink' : 'div'" :to="to" class="panel p-4 block relative overflow-hidden group transition-transform hover:-translate-y-0.5">
    <div class="flex items-start justify-between gap-2">
      <div class="min-w-0">
        <div class="text-[11px] uppercase tracking-wider muted">{{ label }}</div>
        <div class="mono text-2xl font-semibold mt-1.5 truncate" :style="tone ? { color: `var(--${tone === 'accent' ? 'accent-2' : tone})` } : {}">{{ value }}</div>
        <div v-if="hint" class="text-xs muted mt-1 truncate">{{ hint }}</div>
      </div>
      <Spark v-if="spark?.length" :data="spark" :w="84" :h="38" class="shrink-0 opacity-90" />
    </div>
  </component>
</template>
