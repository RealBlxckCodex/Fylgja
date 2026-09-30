<script setup lang="ts">
import { Handle, Position } from '@vue-flow/core'
import Status from './Status.vue'
import { eur } from '@/lib/format'
defineProps<{ data: any }>()
const who: Record<string, string> = { lead: '👑 Lead', member: '🤝', worker: '⚙ Worker', human: '🧑 Mensch' }
</script>
<template>
  <div class="panel px-3 py-2 w-60 text-left shadow" :style="{ borderColor: data.selected ? 'var(--accent-2)' : data.critical ? '#d4313f66' : 'var(--line)' }">
    <Handle type="target" :position="Position.Left" />
    <div class="flex items-center gap-2"><span class="text-sm font-medium truncate flex-1" :title="data.n.title">{{ data.n.title }}</span></div>
    <div class="flex items-center gap-2 mt-1.5"><Status :s="data.n.status" /><span class="text-[11px] muted truncate">{{ who[data.n.owner_kind] }} {{ data.owner || '' }}</span></div>
    <div class="flex items-center justify-between mt-1 text-[11px] muted"><span class="truncate max-w-36" :title="data.n.reason">{{ data.n.reason }}</span><span class="mono">{{ data.cost ? eur(data.cost, 3) : '' }}</span></div>
    <Handle type="source" :position="Position.Right" />
  </div>
</template>
