<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { get, post } from '@/lib/api'
import { useStream } from '@/lib/sse'
import { ago, eur, short } from '@/lib/format'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Status from '@/components/Status.vue'
import Badge from '@/components/Badge.vue'
import Btn from '@/components/Btn.vue'
import Empty from '@/components/Empty.vue'
const toast = useToast()
const dots = ref<any[]>([])
const approvals = ref<any[]>([])
const proposals = ref<any[]>([])
const graphs = ref<any[]>([])
const agents = ref<any[]>([])
const activity = ref<any[]>([])
async function load() {
  const [d, a, p, g, f] = await Promise.all([get('/dots'), get('/approvals?status=pending'), get('/proposals'), get('/graphs'), get('/fleet/overview')])
  dots.value = d; approvals.value = a; proposals.value = p.proposals; graphs.value = g.filter((x: any) => x.status === 'running' || x.status === 'planned'); agents.value = f.agents
}
onMounted(() => load().catch(toast.err))
useStream(() => [...dots.value.map((d) => `dot.${d.dot.id}.activity`), 'approvals', 'proposals'], (topic, d) => {
  if (d?.run) activity.value.unshift({ at: new Date().toISOString(), type: d.type, run: d.run })
  activity.value = activity.value.slice(0, 50)
  load().catch(() => {})
})
async function resolve(a: any, approve: boolean) {
  try { await post(`/approvals/${a.id}/resolve`, { approve }); toast.ok(approve ? 'Freigegeben' : 'Abgelehnt'); load() } catch (e) { toast.err(e) }
}
</script>
<template>
  <div class="space-y-6">
    <div class="flex items-end justify-between"><h1 class="text-xl font-semibold">Command Center</h1></div>
    <div class="grid gap-6 xl:grid-cols-3">
      <Card title="Was läuft gerade" :subtitle="`${agents.length} aktive Läufe · ${graphs.length} Pläne`" class="xl:col-span-2" flush>
        <table v-if="agents.length" class="dense">
          <thead><tr><th>Fylgja</th><th>Art</th><th>Status</th><th>Aufgabe</th><th>Modell-Deployments</th><th class="text-right">Kosten heute</th></tr></thead>
          <tbody>
            <tr v-for="a in agents" :key="a.run_id">
              <td><RouterLink :to="`/dots/${a.dot_id}`" class="hover:underline">{{ a.dot_name }}</RouterLink></td>
              <td class="muted">{{ a.kind }}</td><td><Status :s="a.status" /></td>
              <td class="max-w-80 truncate" :title="a.task">{{ a.task }}</td>
              <td class="mono text-xs">{{ Object.keys(a.deployments).join(', ') || '—' }}</td>
              <td class="text-right mono">{{ eur(a.cost_today_micro_eur) }}</td>
            </tr>
          </tbody>
        </table>
        <Empty v-else>Gerade ist alles ruhig.</Empty>
        <div v-if="graphs.length" class="border-t line p-4 space-y-2">
          <RouterLink v-for="g in graphs" :key="g.id" :to="`/teams/${g.id}`" class="flex items-center gap-3 text-sm hover:underline">
            <Status :s="g.status" /><span class="flex-1 truncate">{{ g.title || short(g.id) }}</span><span class="muted mono">{{ g.done }}/{{ g.nodes }} Knoten · Lead {{ g.lead_name }}</span>
          </RouterLink>
        </div>
      </Card>
      <Card title="Wartet auf dich" :subtitle="`${approvals.length} Freigaben · ${proposals.length} Vorschläge`" flush>
        <ul class="divide-y divide-[var(--line)]">
          <li v-for="a in approvals.slice(0, 8)" :key="a.id" class="p-4 space-y-2">
            <div class="flex items-center gap-2 text-sm"><Badge tone="warn" icon="🛡">{{ a.class }}</Badge><span class="font-medium truncate">{{ a.preview?.summary || a.tool }}</span></div>
            <p class="text-xs muted">{{ a.reason }}</p>
            <div class="flex gap-2">
              <Btn size="sm" @click="resolve(a, true)">Freigeben</Btn>
              <Btn size="sm" variant="ghost" @click="resolve(a, false)">Ablehnen</Btn>
              <RouterLink :to="`/approvals/${a.id}`" class="text-xs muted self-center hover:underline">Details</RouterLink>
            </div>
          </li>
          <li v-for="p in proposals.slice(0, 5)" :key="p.id" class="p-4 text-sm">
            <RouterLink to="/learn" class="flex gap-2 hover:underline"><Badge tone="info" icon="💡">{{ p.type }}</Badge><span class="truncate">{{ p.payload.name || p.payload.text || p.payload.new || 'Vorschlag' }}</span></RouterLink>
          </li>
        </ul>
        <Empty v-if="!approvals.length && !proposals.length">Nichts offen. 🎉</Empty>
      </Card>
    </div>
    <div class="grid gap-6 xl:grid-cols-3">
      <Card title="Fylgjur" class="xl:col-span-2" flush>
        <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-3 p-4">
          <RouterLink v-for="d in dots" :key="d.dot.id" :to="`/dots/${d.dot.id}`" class="panel panel-2 p-4 hover:border-[var(--accent)] block">
            <div class="flex items-center justify-between"><span class="font-medium">{{ d.dot.name }}</span><Status :s="d.dot.status" /></div>
            <div class="text-xs muted mt-1">{{ d.dot.kind }} · L{{ d.dot.autonomy_level }} · {{ d.dot.privacy_mode }}</div>
            <div class="flex gap-3 text-xs mt-3 mono"><span>▶ {{ d.running }}</span><span>⏸ {{ d.waiting }}</span><span>🛡 {{ d.pending_approvals }}</span><span class="ml-auto">{{ eur(d.cost_today_micro_eur) }}</span></div>
          </RouterLink>
        </div>
      </Card>
      <Card title="Was ist passiert" subtitle="Live-Aktivität" flush>
        <ul class="text-sm divide-y divide-[var(--line)] max-h-96 overflow-auto">
          <li v-for="(e, i) in activity" :key="i" class="px-4 py-2 flex gap-2 items-center">
            <Status :s="e.run.status" /><span class="muted">{{ e.run.kind }}</span><span class="truncate flex-1">{{ e.run.input?.text }}</span><span class="text-xs muted whitespace-nowrap">{{ ago(e.at) }}</span>
          </li>
        </ul>
        <Empty v-if="!activity.length">Noch keine Aktivität in dieser Sitzung.</Empty>
      </Card>
    </div>
  </div>
</template>
