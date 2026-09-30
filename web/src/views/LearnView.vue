<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { get, post } from '@/lib/api'
import { dt, pct } from '@/lib/format'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Badge from '@/components/Badge.vue'
import Tabs from '@/components/Tabs.vue'
import Empty from '@/components/Empty.vue'
const toast = useToast()
const tab = ref('open')
const data = ref<any>({ proposals: [], acceptance_rate: 0 })
async function load() { data.value = await get(`/proposals?status=${tab.value}`) }
onMounted(() => load().catch(toast.err))
async function decide(p: any, accept: boolean) {
  try { await post(`/proposals/${p.id}/${accept ? 'accept' : 'reject'}`); toast.ok(accept ? 'Übernommen' : 'Verworfen'); load() } catch (e) { toast.err(e) }
}
const label: Record<string, string> = { rule: 'Regel', preference: 'Präferenz', skill: 'Skill', memory_edit: 'Kernwissen', action: 'Entwurf', pulse_item: 'Pulse' }
</script>
<template>
  <div class="space-y-4">
    <div class="flex items-center gap-3"><h1 class="text-xl font-semibold">Was ich gelernt habe</h1><span class="text-sm muted">Akzeptanzrate <b class="mono">{{ pct(data.acceptance_rate) }}</b></span></div>
    <Tabs v-model="tab" :tabs="[{ id: 'open', label: 'Offen' }, { id: 'accepted', label: 'Angenommen' }, { id: 'rejected', label: 'Abgelehnt' }]" @update:model-value="(v) => { tab = v; load() }" />
    <div class="space-y-3">
      <Card v-for="p in data.proposals" :key="p.id" :title="p.payload.name || label[p.type]" :subtitle="`${p.dot_name} · ${dt(p.created_at)}`">
        <template #actions><Badge tone="info">{{ label[p.type] || p.type }}</Badge></template>
        <div class="space-y-3 text-sm">
          <p v-if="p.payload.text">{{ p.payload.text }}</p>
          <div v-if="p.payload.expr"><div class="text-xs muted">Regel ({{ p.payload.effect }})</div><code class="mono text-xs block panel-2 rounded p-2 mt-1">{{ p.payload.expr }}</code></div>
          <div v-if="p.payload.new" class="grid md:grid-cols-2 gap-2 text-xs">
            <div v-if="p.payload.old" class="panel-2 rounded p-2"><div class="muted">vorher</div><del>{{ p.payload.old }}</del></div>
            <div class="panel-2 rounded p-2"><div class="muted">neu</div>{{ p.payload.new }}</div>
          </div>
          <details v-if="p.payload.body_md"><summary class="cursor-pointer text-xs muted">Skill-Inhalt ({{ p.payload.name }})</summary><pre class="mono text-xs whitespace-pre-wrap mt-2">{{ p.payload.body_md }}</pre></details>
          <details><summary class="cursor-pointer text-xs muted">Evidenz</summary><pre class="mono text-[11px] whitespace-pre-wrap">{{ JSON.stringify(p.evidence, null, 2) }}</pre></details>
          <div v-if="tab === 'open'" class="flex gap-2"><Btn size="sm" @click="decide(p, true)">Annehmen</Btn><Btn size="sm" variant="ghost" @click="decide(p, false)">Ablehnen</Btn></div>
        </div>
      </Card>
      <Empty v-if="!data.proposals.length">Keine Vorschläge. Fylgjur lernen aus Feedback (👍/👎), Korrekturen und wiederholten Freigaben – Änderungen am Verhalten schlagen sie hier vor.</Empty>
    </div>
  </div>
</template>
