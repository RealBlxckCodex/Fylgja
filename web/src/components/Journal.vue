<script setup lang="ts">
// Run-Timeline aus dem Journal (Activity View, 18.4).
import { computed } from 'vue'
import { dt, eur, ms } from '@/lib/format'
import Badge from './Badge.vue'
const props = defineProps<{ data: any }>()
const icon: Record<string, string> = { model_request: '→', model_response: '←', tool_call: '🛠', policy: '⚖', tool_exec: '▶', tool_result: '↩',
  approval_requested: '🛡', approval_resolved: '✔', steer: '✋', checkpoint: '◆', message_out: '💬', taint: '☣', error: '✕', budget_stop: '⛔' }
const events = computed(() => props.data?.events ?? [])
function summary(e: any) {
  const p = e.payload || {}
  switch (e.type) {
    case 'model_request': return `${p.model} (${p.tier}) · ${p.messages} Nachrichten · ${p.tools} Tools · Privacy ${p.privacy}`
    case 'model_response': return (p.message?.tool_calls?.length ? `Tool-Calls: ${p.message.tool_calls.map((t: any) => t.name).join(', ')}` : (p.message?.content || '').slice(0, 300)) + ` · ${p.deployment || '?'} · ${p.usage?.in ?? 0}/${p.usage?.out ?? 0} Tokens${p.degraded ? ' · ERSATZMODELL' : ''}`
    case 'tool_call': return `${p.tool} (${p.class}) ${JSON.stringify(p.args).slice(0, 240)}`
    case 'policy': return `${p.decision?.verdict}: ${(p.decision?.reasons || []).join('; ')}`
    case 'tool_result': return `${p.is_error ? 'FEHLER ' : ''}${p.untrusted ? '[untrusted] ' : ''}${(p.content || '').slice(0, 300)}`
    case 'checkpoint': return `Geladene Memories: ${(p.loaded_memories || []).length} · Präfix ${p.prefix_hash}`
    case 'message_out': return (p.text || '').slice(0, 400)
    case 'taint': return `Kontext enthält untrusted Inhalt aus: ${p.source}`
    default: return JSON.stringify(p).slice(0, 300)
  }
}
</script>
<template>
  <div class="space-y-3">
    <div class="flex flex-wrap gap-3 text-xs muted">
      <span>Tokens <b class="mono text-[var(--text)]">{{ data?.tokens ?? 0 }}</b></span>
      <span>Kosten <b class="mono text-[var(--text)]">{{ eur(data?.cost_micro_eur, 4) }}</b></span>
      <Badge v-if="data?.run?.tainted" tone="err" icon="☣">tainted – riskante Aktionen brauchen Freigabe</Badge>
      <span v-if="data?.run?.error" style="color: var(--err)">{{ data.run.error }}</span>
    </div>
    <ol class="border-l line ml-2 space-y-2">
      <li v-for="e in events" :key="e.seq" class="pl-4 relative">
        <span class="absolute -left-2.5 top-0.5 size-5 rounded-full panel flex items-center justify-center text-[11px]" aria-hidden="true">{{ icon[e.type] || '•' }}</span>
        <div class="text-xs"><span class="font-medium">{{ e.type }}</span> <span class="muted mono">#{{ e.seq }} · {{ dt(e.created_at) }}</span></div>
        <div class="text-xs muted break-words whitespace-pre-wrap">{{ summary(e) }}</div>
      </li>
    </ol>
    <div v-if="data?.routing?.length" class="text-xs">
      <div class="font-medium mb-1">Router-Entscheidungen</div>
      <div v-for="(r, i) in data.routing" :key="i" class="muted mono">{{ r.model }} → {{ r.deployment || '—' }} · {{ r.reason }} · Wartezeit {{ ms(r.queue_wait_ms) }}</div>
    </div>
  </div>
</template>
