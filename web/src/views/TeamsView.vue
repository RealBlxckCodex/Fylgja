<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { get, post } from '@/lib/api'
import { ago, short } from '@/lib/format'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Status from '@/components/Status.vue'
import Modal from '@/components/Modal.vue'
import Tabs from '@/components/Tabs.vue'
import Empty from '@/components/Empty.vue'
import Chart from '@/components/Chart.vue'
const toast = useToast()
const router = useRouter()
const tab = ref('graphs')
const graphs = ref<any[]>([])
const teams = ref<any[]>([])
const dots = ref<any[]>([])
const tasks = ref<any[]>([])
const details = ref<any[]>([])
async function load() {
  ;[graphs.value, teams.value, dots.value, tasks.value] = await Promise.all([get('/graphs'), get('/teams'), get('/dots'), get('/tasks')])
  details.value = (await Promise.all(graphs.value.slice(0, 20).map((g) => get(`/graphs/${g.id}`).catch(() => null)))).filter(Boolean)
}
onMounted(() => load().catch(toast.err))
// Delegations-Insights (18.6 E)
const insights = computed(() => {
  const by: Record<string, number> = { lead: 0, member: 0, worker: 0, human: 0 }
  const fail: Record<string, number> = {}
  let retries = 0, nodes = 0
  for (const d of details.value) for (const n of d.graph.nodes) {
    by[n.owner_kind] = (by[n.owner_kind] || 0) + 1; nodes++
    retries += Math.max(0, n.attempt - 1)
    if (n.status === 'failed') { const k = (n.reason || 'unbekannt').split(':')[0]; fail[k] = (fail[k] || 0) + 1 }
  }
  return { by, fail, retries, nodes }
})
const pie = computed(() => ({ tooltip: { trigger: 'item' }, series: [{ type: 'pie', radius: ['45%', '70%'], label: { color: 'inherit' },
  data: [{ name: 'Selbst (Lead)', value: insights.value.by.lead }, { name: 'Teammates', value: insights.value.by.member }, { name: 'Worker', value: insights.value.by.worker }, { name: 'Mensch', value: insights.value.by.human }] }] }))
// Team anlegen
const teamOpen = ref(false)
const tf = ref({ name: '', lead_dot_id: '', members: [] as string[], charter: '' })
async function createTeam() { try { await post('/teams', tf.value); teamOpen.value = false; load() } catch (e) { toast.err(e) } }
// Plan anlegen (Vorlage)
const planOpen = ref(false)
const planLead = ref('')
const planJSON = ref(JSON.stringify({
  title: 'Hosting-Vergleich', budget: { tokens: 600000 },
  nodes: [
    { id: 'req', title: 'Anforderungen klären', owner_kind: 'lead', contract: { goal: 'Anforderungen aus dem Memory zusammenfassen', output_schema: { type: 'object', properties: { requirements: { type: 'array', items: { type: 'string' } } }, required: ['requirements'] }, tool_scope: ['memory.search'], budget: { tokens: 100000, wall_clock_s: 600 } } },
    { id: 'research', title: 'Anbieter recherchieren', owner_kind: 'worker', contract: { goal: 'Drei Anbieter mit Preisen (Stand < 7 Tage) finden', output_schema: { type: 'object', properties: { options: { type: 'array' }, sources: { type: 'array' } }, required: ['options', 'sources'] }, acceptance: ['Mindestens 3 Anbieter', 'Jede Aussage mit Quelle'], tool_scope: ['web.search', 'web.fetch'], budget: { tokens: 300000, wall_clock_s: 1800 } } },
    { id: 'rec', title: 'Empfehlung schreiben', owner_kind: 'lead', contract: { goal: 'Empfehlung mit Begründung', output_schema: { type: 'object', properties: { recommendation: { type: 'string' } }, required: ['recommendation'] }, tool_scope: [], budget: { tokens: 150000, wall_clock_s: 600 } } },
  ],
  edges: [{ from: 'req', to: 'research', kind: 'depends_on' }, { from: 'research', to: 'rec', kind: 'depends_on' }],
}, null, 2))
async function createPlan() {
  try {
    const g = JSON.parse(planJSON.value)
    g.lead_dot_id = planLead.value
    for (const n of g.nodes) if (n.owner_kind === 'lead') n.owner_dot_id = planLead.value
    const res = await post('/graphs', g)
    planOpen.value = false
    router.push(`/teams/${res.id}`)
  } catch (e) { toast.err(e) }
}
</script>
<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between gap-2 flex-wrap">
      <h1 class="text-xl font-semibold">Tasks & Teams</h1>
      <div class="flex gap-2"><Btn variant="ghost" @click="teamOpen = true">+ Team</Btn><Btn @click="planOpen = true; planLead = dots[0]?.dot.id">+ Plan</Btn></div>
    </div>
    <Tabs v-model="tab" :tabs="[{ id: 'graphs', label: 'Pläne', badge: graphs.filter((g) => g.status === 'running').length || undefined }, { id: 'teams', label: 'Teams' }, { id: 'tasks', label: 'Aufgaben' }, { id: 'insights', label: 'Delegations-Insights' }]" />
    <Card v-if="tab === 'graphs'" flush>
      <table v-if="graphs.length" class="dense">
        <thead><tr><th>Plan</th><th>Status</th><th>Lead</th><th>Fortschritt</th><th>Erstellt</th></tr></thead>
        <tbody><tr v-for="g in graphs" :key="g.id" class="cursor-pointer" @click="router.push(`/teams/${g.id}`)">
          <td>{{ g.title || short(g.id) }}</td><td><Status :s="g.status" /></td><td>{{ g.lead_name }}</td>
          <td><div class="flex items-center gap-2"><div class="h-1.5 w-32 rounded bg-[var(--panel-2)] overflow-hidden"><div class="h-full bg-accent" :style="{ width: (g.nodes ? (100 * g.done) / g.nodes : 0) + '%' }" /></div><span class="mono text-xs">{{ g.done }}/{{ g.nodes }}</span></div></td>
          <td class="muted">{{ ago(g.created_at) }}</td></tr></tbody>
      </table>
      <Empty v-else>Noch keine Pläne. Bitte deine Lead-Fylgja im Chat um ein großes Ziel – sie erstellt mit <code>team.plan</code> einen Plan, oder lege hier einen an.</Empty>
    </Card>
    <div v-else-if="tab === 'teams'" class="grid gap-4 md:grid-cols-2">
      <Card v-for="t in teams" :key="t.id" :title="t.name" :subtitle="`Lead: ${t.lead_name}`">
        <p v-if="t.charter" class="text-sm muted mb-3 whitespace-pre-wrap">{{ t.charter }}</p>
        <ul class="text-sm space-y-1"><li v-for="m in t.members" :key="m.dot_id" class="flex gap-2"><span class="w-20 muted">{{ m.role }}</span><RouterLink :to="`/dots/${m.dot_id}`" class="hover:underline">{{ m.name }}</RouterLink><span class="ml-auto mono text-xs muted">{{ m.open_nodes }} offen</span></li></ul>
      </Card>
      <Empty v-if="!teams.length">Keine Teams. Teams bündeln eine Lead-Fylgja mit Teammates (jede behält ihre eigene Identität und Rechte).</Empty>
    </div>
    <Card v-else-if="tab === 'tasks'" flush>
      <table class="dense"><thead><tr><th>Aufgabe</th><th>Fylgja</th><th>Status</th><th>Priorität</th><th>Fällig</th><th>Aktualisiert</th></tr></thead>
        <tbody><tr v-for="t in tasks" :key="t.id"><td>{{ t.title }}</td><td>{{ t.dot_name }}</td><td><Status :s="t.status" /></td><td class="mono">P{{ t.priority }}</td><td class="muted">{{ t.due_at ? new Date(t.due_at).toLocaleString('de-DE') : '—' }}</td><td class="muted">{{ ago(t.updated_at) }}</td></tr></tbody></table>
    </Card>
    <div v-else class="grid gap-4 md:grid-cols-2">
      <Card title="Verteilung Selbst / Teammate / Worker" :subtitle="`${insights.nodes} Knoten in ${details.length} Plänen`"><Chart :option="pie" height="260px" /></Card>
      <Card title="Qualität">
        <div class="text-sm space-y-2">
          <div>Retries gesamt: <b class="mono">{{ insights.retries }}</b></div>
          <div class="font-medium mt-3">Häufigste Fehlerursachen</div>
          <div v-for="(n, k) in insights.fail" :key="k" class="flex gap-2"><span class="flex-1 truncate">{{ k }}</span><span class="mono">{{ n }}</span></div>
          <p v-if="!Object.keys(insights.fail).length" class="muted">Keine fehlgeschlagenen Knoten.</p>
          <p v-if="insights.by.worker && Object.keys(insights.fail).length" class="text-xs muted mt-3">Tipp: Scheitern Worker bei einer Aufgabenklasse häufig, weise sie dem Lead selbst oder einem stärkeren Tier zu.</p>
        </div>
      </Card>
    </div>
    <Modal :open="teamOpen" title="Team anlegen" @close="teamOpen = false">
      <form class="space-y-3" @submit.prevent="createTeam">
        <input v-model="tf.name" class="input" placeholder="Name" required />
        <select v-model="tf.lead_dot_id" class="input" required><option value="" disabled>Lead wählen</option><option v-for="d in dots" :key="d.dot.id" :value="d.dot.id">{{ d.dot.name }}</option></select>
        <div class="text-xs muted">Mitglieder</div>
        <label v-for="d in dots" :key="d.dot.id" class="flex gap-2 text-sm"><input v-model="tf.members" type="checkbox" :value="d.dot.id" />{{ d.dot.name }}</label>
        <textarea v-model="tf.charter" class="input" rows="3" placeholder="Charter (optional)" />
        <div class="flex justify-end"><Btn type="submit">Anlegen</Btn></div>
      </form>
    </Modal>
    <Modal :open="planOpen" title="Plan anlegen" wide @close="planOpen = false">
      <div class="space-y-3">
        <select v-model="planLead" class="input"><option v-for="d in dots" :key="d.dot.id" :value="d.dot.id">Lead: {{ d.dot.name }}</option></select>
        <p class="text-xs muted">Verträge: goal, output_schema (JSON-Schema), acceptance, tool_scope (Teilmenge der Lead-Rechte), budget. Worker können nie mehr als ihr Auftraggeber.</p>
        <textarea v-model="planJSON" class="input mono text-xs" rows="22" />
        <div class="flex justify-end"><Btn @click="createPlan">Plan prüfen & anlegen</Btn></div>
      </div>
    </Modal>
  </div>
</template>
