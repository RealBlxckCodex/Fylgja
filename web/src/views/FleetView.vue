<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { get, post, put } from '@/lib/api'
import { subscribe } from '@/lib/sse'
import { eur, pct, ms, ago, short, dt } from '@/lib/format'
import { useToast } from '@/stores/toast'
import { useAuth } from '@/stores/auth'
import Card from '@/components/Card.vue'
import Tabs from '@/components/Tabs.vue'
import Status from '@/components/Status.vue'
import Badge from '@/components/Badge.vue'
import Btn from '@/components/Btn.vue'
import Chart from '@/components/Chart.vue'
import FlowTopology from '@/components/FlowTopology.vue'
import Gauge from '@/components/Gauge.vue'
import Empty from '@/components/Empty.vue'
import Modal from '@/components/Modal.vue'
const toast = useToast()
const auth = useAuth()
const tab = ref('topology')
const fo = ref<any>(null)
const filter = ref('')
const queueHist = ref<{ t: number; q: Record<string, number>; p95: number }[]>([])
async function load() { fo.value = await get('/fleet/overview') }
let close: (() => void) | undefined
let timer: number | undefined
onMounted(() => {
  load().catch(toast.err)
  timer = window.setInterval(() => load().catch(() => {}), 5000)
  close = subscribe(['router.queues'], (_t, d) => {
    if (!fo.value) return
    fo.value.router = d.router
    const q: Record<string, number> = {}
    let p95 = 0
    for (const x of d.router.queues) { q[x.class] = x.depth; p95 = Math.max(p95, x.wait_p95_ms) }
    queueHist.value.push({ t: Date.now(), q, p95 })
    if (queueHist.value.length > 300) queueHist.value.shift()
  })
})
onBeforeUnmount(() => { close?.(); clearInterval(timer) })
const s = computed(() => fo.value?.summary ?? {})
const deps = computed(() => fo.value?.router?.deployments ?? [])
const agents = computed(() => (fo.value?.agents ?? []).filter((a: any) => !filter.value || JSON.stringify(a).toLowerCase().includes(filter.value.toLowerCase())))
const queueChart = computed(() => {
  const classes = ['interactive', 'review', 'task', 'background']
  return { legend: { data: classes, textStyle: { color: '#aaa' } }, xAxis: { type: 'time' }, yAxis: { type: 'value', name: 'Wartend', minInterval: 1 },
    series: classes.map((c) => ({ name: c, type: 'line', stack: 'q', areaStyle: {}, showSymbol: false, data: queueHist.value.map((h) => [h.t, h.q[c] ?? 0]) })) }
})
const costChart = computed(() => ({ xAxis: { type: 'category', data: ['Flotte gesamt', 'davon zugeordnet', 'Leerlauf (Overhead)', 'Kosten gesamt heute'] }, yAxis: { type: 'value', name: '€' },
  series: [{ type: 'bar', data: [s.value.fleet_spend_today_micro_eur, Math.max(0, s.value.fleet_spend_today_micro_eur - s.value.fleet_overhead_micro_eur), s.value.fleet_overhead_micro_eur, s.value.cost_today_micro_eur].map((v) => ((v || 0) / 1e6).toFixed(2)) }] }))
const whatIf = computed(() => {
  const ready = s.value.nodes_ready || 0
  const p95 = s.value.queue_wait_p95_ms || 0
  const perHour = (fo.value?.nodes || []).find((n: any) => n.state === 'ready')?.hourly_cost_micro_eur || 0
  return { p95: ready ? (p95 * ready) / (ready + 1) : 0, delta: perHour }
})
async function nodeAction(n: any, action: string) {
  if (!confirm(`${action === 'terminate' ? 'Node beenden' : 'Node leeren'}: ${n.name}?`)) return
  try { await post(`/fleet/nodes/${n.id}/${action}`); toast.ok('OK'); load() } catch (e) { toast.err(e) }
}
// Policies
const polOpen = ref(false)
const pol = ref<any>({ name: 'main', model: 'worker-default', min_nodes: 0, max_nodes: 2, daily_budget_micro_eur: 20000000, hard: true, enabled: true,
  scale_up: { wait_p95_ms: 10000, kv_util: 0.85, window: 120e9 }, scale_down: { util: 0.15, window: 1200e9 }, cooldown: 300e9,
  spec: { gpu_type: 'NVIDIA L40S', gpu_count: 1, image: 'ghcr.io/realblxckcodex/fylgja-node:latest', network_volume_id: '', cloud_type: 'SECURE', region: 'EU-RO-1' } })
async function savePolicy() { try { await put('/fleet/policies', pol.value); polOpen.value = false; toast.ok('Policy gespeichert'); load() } catch (e) { toast.err(e) } }
async function provision(p: any) { try { await post(`/fleet/pools/${p.name}/provision`); toast.ok('Pod wird gestartet'); load() } catch (e) { toast.err(e) } }
const addOpen = ref(false)
const addName = ref('')
const added = ref<any>(null)
async function addNode() { try { added.value = await post('/fleet/nodes', { name: addName.value }); load() } catch (e) { toast.err(e) } }
</script>
<template>
  <div v-if="fo" class="space-y-4">
    <div class="flex items-center gap-3 flex-wrap">
      <h1 class="text-xl font-semibold">Flotte</h1>
      <div class="flex-1" />
      <Btn v-if="auth.can('own')" variant="ghost" size="sm" @click="addOpen = true">+ Lokaler Node</Btn>
      <Btn v-if="auth.can('own')" variant="ghost" size="sm" @click="polOpen = true">Scaling-Policy</Btn>
    </div>
    <div class="grid grid-cols-2 md:grid-cols-4 xl:grid-cols-7 gap-3">
      <div v-for="k in [['GPU-Auslastung', pct(s.gpu_util)], ['Nodes ready', `${s.nodes_ready}/${s.nodes_total}`], ['Queue p95', ms(s.queue_wait_p95_ms)], ['Kosten heute', eur(s.cost_today_micro_eur)],
        ['Flotte heute', eur(s.fleet_spend_today_micro_eur)], ['Leerlauf-Overhead', eur(s.fleet_overhead_micro_eur)], ['Sandboxen', `${s.sandboxes_running} ▶ · ${s.sandboxes_sleeping} ☾`]]" :key="k[0]" class="panel p-4">
        <div class="text-[11px] uppercase tracking-wider muted">{{ k[0] }}</div><div class="mono text-xl mt-1.5">{{ k[1] }}</div>
      </div>
    </div>
    <Tabs v-model="tab" :tabs="[{ id: 'topology', label: 'Topologie' }, { id: 'nodes', label: 'Nodes', badge: fo.nodes.length || undefined }, { id: 'router', label: 'Queue & Routing' },
      { id: 'agents', label: 'Agenten', badge: fo.agents.length || undefined }, { id: 'capacity', label: 'Kapazität & Kosten' }, { id: 'hosts', label: 'Sandbox-Hosts' }]" />

    <Card v-if="tab === 'topology'" title="Wo läuft welcher Agent?" subtitle="Agenten → Sandbox-Hosts → Modell-Deployments · Linienstärke = Modellaufrufe (5 min) · orange = Circuit Breaker offen · Hover hebt Pfad hervor">
      <FlowTopology v-if="fo.agents.length || deps.length" :agents="fo.agents" :deployments="deps" /><Empty v-else>Keine aktiven Agenten und keine Deployments.</Empty>
    </Card>

    <div v-else-if="tab === 'nodes'" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <div v-for="n in fo.nodes" :key="n.id" class="panel p-5 space-y-4 rise">
        <div class="flex items-center gap-2"><div class="min-w-0 flex-1"><div class="font-semibold truncate">{{ n.name }}</div><div class="text-xs muted truncate">{{ n.provider }} · {{ n.gpu_model || 'CPU' }}<span v-if="n.gpu_count"> ×{{ n.gpu_count }}</span> · {{ n.region || 'Region ?' }}</div></div><Status :s="n.state" /></div>
        <div class="flex justify-between">
          <div v-for="m in [['GPU', n.metrics.gpu_util], ['VRAM', n.metrics.vram_total_mb ? n.metrics.vram_used_mb / n.metrics.vram_total_mb : 0], ['KV', n.metrics.kv_cache_util]]" :key="m[0] as string" class="text-center"><Gauge :value="m[1] as number" :size="78" :label="m[0] as string" /></div>
        </div>
        <div class="grid grid-cols-3 gap-3 text-xs mono">
          <div><div class="muted font-sans text-[11px]">Requests</div>{{ n.metrics.running_reqs }} ▶ {{ n.metrics.queued_reqs }} ◷</div>
          <div><div class="muted font-sans text-[11px]">Tokens/s</div>{{ Math.round(n.metrics.tokens_per_s || 0) }}</div>
          <div><div class="muted font-sans text-[11px]">Temp · Leistung</div>{{ Math.round(n.metrics.temp_c || 0) }}° · {{ Math.round(n.metrics.power_w || 0) }} W</div>
          <div><div class="muted font-sans text-[11px]">Kosten</div>{{ eur(n.hourly_cost_micro_eur) }}/h</div>
          <div><div class="muted font-sans text-[11px]">Heartbeat</div>{{ ago(n.last_heartbeat) }}</div>
          <div><div class="muted font-sans text-[11px]">Prefix-Hits</div>{{ pct(n.metrics.prefix_hit_rate) }}</div>
        </div>
        <div class="flex flex-wrap gap-1.5"><Badge v-for="d in n.deployments || []" :key="d.served_model" tone="info">{{ d.model }} ← {{ d.served_model }}</Badge></div>
        <div v-if="auth.can('own') && n.state !== 'gone'" class="flex gap-2"><Btn size="sm" variant="ghost" @click="nodeAction(n, 'drain')">Drain</Btn><Btn size="sm" variant="danger" @click="nodeAction(n, 'terminate')">Beenden</Btn></div>
      </div>
      <Empty v-if="!fo.nodes.length">Keine Nodes. Lege eine Scaling-Policy für RunPod an oder registriere einen lokalen Node (Ollama/llama.cpp).</Empty>
    </div>

    <div v-else-if="tab === 'router'" class="space-y-4">
      <Card title="Warteschlange je Prioritätsklasse" subtitle="Live (SSE, 1 s)"><Chart :option="queueChart" height="240px" /></Card>
      <div class="grid gap-4 xl:grid-cols-3">
        <Card title="Wartezeiten" flush>
          <table class="dense"><thead><tr><th>Klasse</th><th>Wartend</th><th>p50</th><th>p95</th></tr></thead>
            <tbody><tr v-for="q in fo.router.queues" :key="q.class"><td>{{ q.class }}</td><td class="mono">{{ q.depth }}</td><td class="mono">{{ ms(q.wait_p50_ms) }}</td><td class="mono">{{ ms(q.wait_p95_ms) }}</td></tr></tbody></table>
        </Card>
        <Card title="Deployments" class="xl:col-span-2" flush>
          <table class="dense"><thead><tr><th>Deployment</th><th>Modell</th><th>Provider</th><th>Region</th><th>Status</th><th>Breaker</th><th>Slots</th><th>p95</th><th>OK/Fehler</th></tr></thead>
            <tbody><tr v-for="d in deps" :key="d.name"><td class="mono text-xs">{{ d.name }}</td><td>{{ d.model }}</td><td>{{ d.provider }}</td><td class="muted">{{ d.region || '—' }}</td><td><Status :s="d.state" /></td><td><Status :s="d.breaker" /></td>
              <td class="mono">{{ d.inflight }}/{{ d.max_concurrency }}</td><td class="mono">{{ ms(d.latency_p95_ms) }}</td><td class="mono">{{ d.served }}/{{ d.errors }}</td></tr></tbody></table>
        </Card>
      </div>
      <Card title="Modell-Katalog" subtitle="Logische Modelle → Deployments (Privacy-Klasse, Fallback-Kette)" flush>
        <table class="dense"><thead><tr><th>Logisches Modell</th><th>Privacy</th><th>Qualität</th><th>Fähigkeiten</th><th>Fallbacks</th></tr></thead>
          <tbody><tr v-for="m in fo.router.models" :key="m.name"><td class="mono">{{ m.name }}</td><td>{{ m.privacy_class }}</td><td class="mono">{{ m.quality_rank }}</td><td class="text-xs">{{ (m.capabilities || []).join(', ') }}</td><td class="text-xs mono">{{ (m.fallbacks || []).join(' → ') || '—' }}</td></tr></tbody></table>
      </Card>
    </div>

    <Card v-else-if="tab === 'agents'" title="Laufende Agenten" flush>
      <template #actions><input v-model="filter" class="input !py-1 !w-60 text-xs" placeholder="Filter, z. B. runpod-A" aria-label="Filter" /></template>
      <table class="dense"><thead><tr><th>Fylgja</th><th>Rolle</th><th>Status</th><th>Aufgabe</th><th>Graph</th><th>Sandbox-Host</th><th>Deployments (5 min)</th><th>Tokens/min</th><th class="text-right">Kosten heute</th></tr></thead>
        <tbody><tr v-for="a in agents" :key="a.run_id">
          <td><RouterLink :to="`/dots/${a.dot_id}`" class="hover:underline">{{ a.dot_name }}</RouterLink></td><td class="muted">{{ a.role || a.kind }}</td><td><Status :s="a.status" /></td>
          <td class="max-w-72 truncate" :title="a.task">{{ a.task }}</td><td><RouterLink v-if="a.graph" :to="`/teams/${a.graph}`" class="mono text-xs hover:underline">{{ short(a.graph) }}</RouterLink></td>
          <td class="muted">{{ a.sandbox_host || '—' }}</td><td class="text-xs mono">{{ Object.entries(a.deployments).map(([k, v]) => `${k}×${v}`).join(', ') || '—' }}</td>
          <td class="mono">{{ Math.round(a.tokens_per_min) }}</td><td class="text-right mono">{{ eur(a.cost_today_micro_eur) }}</td></tr></tbody></table>
      <Empty v-if="!agents.length">Keine laufenden Agenten.</Empty>
    </Card>

    <div v-else-if="tab === 'capacity'" class="grid gap-4 xl:grid-cols-2">
      <Card title="Kosten heute" subtitle="Bezahlte, ungenutzte GPU-Zeit wird separat als Overhead ausgewiesen"><Chart :option="costChart" height="260px" /></Card>
      <Card title="Was wäre wenn …">
        <p class="text-sm">Ein zusätzlicher Node würde die Queue-Wartezeit p95 grob von <b class="mono">{{ ms(s.queue_wait_p95_ms) }}</b> auf <b class="mono">{{ ms(whatIf.p95) }}</b> senken und etwa <b class="mono">{{ eur(whatIf.delta) }}/h</b> kosten.</p>
        <div class="mt-4 space-y-2"><div v-for="p in fo.policies" :key="p.name" class="panel panel-2 p-3 text-sm flex items-center gap-3">
          <div class="flex-1"><b>{{ p.name }}</b> <span class="muted">· {{ p.model }} · {{ p.min_nodes }}–{{ p.max_nodes }} Nodes · Budget {{ eur(p.daily_budget_micro_eur) }}/Tag {{ p.hard ? '(hart)' : '' }}</span></div>
          <Btn v-if="auth.can('own')" size="sm" @click="provision(p)">+ Node starten</Btn></div></div>
      </Card>
      <Card title="Scale-Events" class="xl:col-span-2" flush>
        <table class="dense"><thead><tr><th>Zeit</th><th>Pool</th><th>Aktion</th><th>Node</th><th>Grund</th></tr></thead>
          <tbody><tr v-for="(e, i) in [...fo.scale_events].reverse()" :key="i"><td class="muted">{{ dt(e.at) }}</td><td>{{ e.pool }}</td><td><Badge :tone="e.kind === 'scale_up' ? 'info' : 'warn'">{{ e.kind }}</Badge></td><td class="mono text-xs">{{ short(e.node_id) }}</td><td>{{ e.reason }}</td></tr></tbody></table>
        <Empty v-if="!fo.scale_events.length">Noch keine Skalierungsereignisse.</Empty>
      </Card>
    </div>

    <Card v-else title="Sandbox-Hosts" flush>
      <table class="dense"><thead><tr><th>Host</th><th>Provider</th><th>Status</th><th>Laufend</th><th>Schlafend</th></tr></thead>
        <tbody><tr v-for="h in fo.sandbox_hosts" :key="h.name"><td>{{ h.name }}</td><td>{{ h.provider }}</td><td><Status :s="h.status === 'ready' ? 'active' : h.status" /></td><td class="mono">{{ h.running }}</td><td class="mono">{{ h.sleeping }}</td></tr></tbody></table>
      <Empty v-if="!fo.sandbox_hosts.length">Sandbox-Hosts werden über <code>sandbox.hosts</code> konfiguriert. Aktuell {{ s.sandboxes_running }} laufende / {{ s.sandboxes_sleeping }} schlafende Sandboxen.</Empty>
    </Card>

    <Modal :open="polOpen" title="Scaling-Policy (RunPod)" wide @close="polOpen = false">
      <form class="grid grid-cols-2 gap-3 text-sm" @submit.prevent="savePolicy">
        <div><label class="text-xs muted">Name</label><input v-model="pol.name" class="input" /></div>
        <div><label class="text-xs muted">Logisches Modell</label><input v-model="pol.model" class="input mono" /></div>
        <div><label class="text-xs muted">Min. Nodes</label><input v-model.number="pol.min_nodes" type="number" min="0" class="input" /></div>
        <div><label class="text-xs muted">Max. Nodes</label><input v-model.number="pol.max_nodes" type="number" min="0" class="input" /></div>
        <div><label class="text-xs muted">Tagesbudget (µ€)</label><input v-model.number="pol.daily_budget_micro_eur" type="number" class="input mono" /></div>
        <label class="flex items-center gap-2 mt-5"><input v-model="pol.hard" type="checkbox" /> Hartes Limit (Drain bei Überschreitung)</label>
        <div><label class="text-xs muted">GPU-Typ</label><input v-model="pol.spec.gpu_type" class="input" /></div>
        <div><label class="text-xs muted">Region (für eu_only)</label><input v-model="pol.spec.region" class="input" /></div>
        <div class="col-span-2"><label class="text-xs muted">Image</label><input v-model="pol.spec.image" class="input mono" /></div>
        <div><label class="text-xs muted">Network Volume (Modellgewichte)</label><input v-model="pol.spec.network_volume_id" class="input mono" /></div>
        <div><label class="text-xs muted">Cloud-Typ</label><select v-model="pol.spec.cloud_type" class="input"><option>SECURE</option><option value="COMMUNITY">COMMUNITY (nur background)</option></select></div>
        <label class="flex items-center gap-2"><input v-model="pol.enabled" type="checkbox" /> Autoscaling aktiv</label>
        <div class="col-span-2 flex justify-end"><Btn type="submit">Speichern (Step-up)</Btn></div>
      </form>
    </Modal>
    <Modal :open="addOpen" title="Lokalen Inference-Node registrieren" @close="addOpen = false; added = null">
      <form v-if="!added" class="flex gap-2" @submit.prevent="addNode"><input v-model="addName" class="input" placeholder="z. B. homelab-ollama" required /><Btn type="submit">Anlegen</Btn></form>
      <div v-else class="text-xs space-y-2"><p class="muted">Auf dem Server setzen und <code>fylgja-node</code> starten (Token wird nur einmal angezeigt):</p>
        <pre class="mono panel-2 rounded p-3 whitespace-pre-wrap break-all">{{ Object.entries(added.env).map(([k, v]) => `export ${k}='${v}'`).join('\n') }}</pre></div>
    </Modal>
  </div>
</template>
