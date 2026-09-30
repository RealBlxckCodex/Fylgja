<script setup lang="ts">
import { ref, computed, onMounted, markRaw } from 'vue'
import { VueFlow, MarkerType, type Node, type Edge } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import { get, post } from '@/lib/api'
import { useStream } from '@/lib/sse'
import { eur, dt, short } from '@/lib/format'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Status from '@/components/Status.vue'
import Badge from '@/components/Badge.vue'
import Tabs from '@/components/Tabs.vue'
import Chart from '@/components/Chart.vue'
import GraphNode from '@/components/GraphNode.vue'
const props = defineProps<{ id: string }>()
const toast = useToast()
const data = ref<any>(null)
const sel = ref<string | null>(null)
const tab = ref('graph')
const steerText = ref('')
const humanResult = ref('{}')
const nodeTypes = { work: markRaw(GraphNode) } as any
async function load() { data.value = await get(`/graphs/${props.id}`) }
onMounted(() => load().catch(toast.err))
useStream(() => [`graph.${props.id}`], () => load().catch(() => {}))
const g = computed(() => data.value?.graph)
const node = computed(() => g.value?.nodes.find((n: any) => n.id === sel.value))
// Layout: Ebenen nach Abhängigkeitstiefe.
const layout = computed(() => {
  if (!g.value) return { nodes: [] as Node[], edges: [] as Edge[] }
  const deps: Record<string, string[]> = {}
  for (const e of g.value.edges || []) (deps[e.to] ||= []).push(e.from)
  const level: Record<string, number> = {}
  const lv = (id: string, seen = new Set<string>()): number => {
    if (level[id] != null) return level[id]
    if (seen.has(id)) return 0
    seen.add(id)
    return (level[id] = Math.max(-1, ...(deps[id] || []).map((d) => lv(d, seen))) + 1)
  }
  g.value.nodes.forEach((n: any) => lv(n.id))
  const rows: Record<number, number> = {}
  const cp = new Set(data.value.critical_path || [])
  const nodes: Node[] = g.value.nodes.map((n: any) => {
    const l = level[n.id]; const r = (rows[l] = (rows[l] ?? -1) + 1)
    return { id: n.id, type: 'work', position: { x: l * 290, y: r * 130 }, data: { n, cost: data.value.costs?.[n.id], owner: data.value.dot_names?.[n.owner_dot_id], critical: cp.has(n.id), selected: sel.value === n.id } }
  })
  const edges: Edge[] = (g.value.edges || []).map((e: any) => ({ id: `${e.from}-${e.to}-${e.kind}`, source: e.from, target: e.to, animated: g.value.nodes.find((n: any) => n.id === e.to)?.status === 'running',
    markerEnd: MarkerType.ArrowClosed, style: { stroke: e.kind === 'reviews' ? '#e6b422' : cp.has(e.from) && cp.has(e.to) ? '#d4313f' : '#555' }, label: e.kind === 'depends_on' ? '' : e.kind }))
  return { nodes, edges }
})
async function act(path: string, body?: any) {
  try { await post(`/graphs/${props.id}${path}`, body); toast.ok('OK'); await load() } catch (e) { toast.err(e) }
}
// Timeline aus der Zustands-Historie (18.6 B)
const timeline = computed(() => {
  if (!data.value) return null
  const cats: string[] = []; const items: any[] = []
  for (const h of data.value.history || []) {
    const n = g.value.nodes.find((x: any) => x.id === h.node); if (!n) continue
    cats.push(n.title)
    let start: number | null = null
    for (const ev of h.history || []) {
      const t = new Date(ev.at).getTime()
      if (ev.to === 'running') start = t
      if (start && ['needs_review', 'failed', 'blocked', 'cancelled', 'done', 'ready'].includes(ev.to)) { items.push({ value: [cats.length - 1, start, t, ev.to], itemStyle: { color: ev.to === 'failed' ? '#f05a28' : '#d4313f' } }); start = null }
    }
    if (start) items.push({ value: [cats.length - 1, start, Date.now(), 'running'], itemStyle: { color: '#5b8def' } })
  }
  return {
    tooltip: { formatter: (p: any) => `${cats[p.value[0]]}: ${Math.round((p.value[2] - p.value[1]) / 1000)} s` },
    grid: { left: 160, right: 20, top: 10, bottom: 30 }, xAxis: { type: 'time' }, yAxis: { type: 'category', data: cats },
    series: [{ type: 'custom', encode: { x: [1, 2], y: 0 }, data: items, renderItem: (_: any, api: any) => {
      const s = api.coord([api.value(1), api.value(0)]); const e = api.coord([api.value(2), api.value(0)]); const h = api.size([0, 1])[1] * 0.5
      return { type: 'rect', shape: { x: s[0], y: s[1] - h / 2, width: Math.max(2, e[0] - s[0]), height: h }, style: api.style() }
    } }],
  }
})
</script>
<template>
  <div v-if="g" class="space-y-4">
    <div class="flex flex-wrap items-center gap-3">
      <RouterLink to="/teams" class="muted text-sm hover:underline">← Tasks & Teams</RouterLink>
      <h1 class="text-xl font-semibold">{{ g.title || short(g.id) }}</h1><Status :s="g.status" />
      <span class="text-xs muted">Plan v{{ g.plan_version }} · Restbudget <span class="mono">{{ data.remaining.tokens > 0 ? data.remaining.tokens.toLocaleString('de-DE') + ' Tokens' : '—' }}</span></span>
      <div class="flex-1" />
      <Btn v-if="g.status === 'planned'" @click="act('/start')">▶ Plan starten</Btn>
      <Btn v-if="g.status === 'running' || g.status === 'planned'" variant="danger" @click="act('/cancel')">Plan abbrechen</Btn>
    </div>
    <Card v-if="g.status === 'planned'" title="Plan-Vorschau" subtitle="Prüfe Kosten, Dauer, Freigaben und Delegationsentscheidungen vor dem Start">
      <div class="grid sm:grid-cols-4 gap-4 text-sm">
        <div><div class="muted text-xs">Knoten</div><div class="mono text-lg">{{ data.estimate.nodes }}</div><div class="text-xs muted">{{ data.estimate.self_nodes }} selbst · {{ data.estimate.delegated_nodes }} delegiert</div></div>
        <div><div class="muted text-xs">Kosten (Schätzung)</div><div class="mono text-lg">{{ data.estimate.cost_min_eur.toFixed(2) }}–{{ data.estimate.cost_max_eur.toFixed(2) }} €</div></div>
        <div><div class="muted text-xs">Dauer (kritischer Pfad)</div><div class="mono text-lg">~{{ data.estimate.minutes }} min</div></div>
        <div><div class="muted text-xs">Voraussichtliche Freigaben</div><div v-for="a in data.estimate.needs_approvals || []" :key="a" class="text-xs">🛡 {{ a }}</div><div v-if="!(data.estimate.needs_approvals || []).length" class="text-xs muted">keine</div></div>
      </div>
      <div class="mt-4 text-xs space-y-1"><div v-for="n in g.nodes.filter((x: any) => x.rationale)" :key="n.id"><b>{{ n.title }}</b>: {{ n.rationale }}</div></div>
    </Card>
    <Tabs v-model="tab" :tabs="[{ id: 'graph', label: 'Graph' }, { id: 'timeline', label: 'Timeline' }]" />
    <div v-if="tab === 'graph'" class="grid gap-4 xl:grid-cols-3">
      <div class="panel xl:col-span-2 h-[65vh] overflow-hidden">
        <VueFlow :nodes="layout.nodes" :edges="layout.edges" :node-types="nodeTypes" fit-view-on-init :min-zoom="0.2" @node-click="(e: any) => (sel = e.node.id)">
          <Background pattern-color="#333" :gap="20" /><Controls />
        </VueFlow>
      </div>
      <Card :title="node ? node.title : 'Knoten wählen'" :subtitle="node ? `${node.owner_kind} · Versuch ${node.attempt}` : ''">
        <div v-if="node" class="space-y-4 text-sm">
          <div class="flex gap-2 flex-wrap"><Status :s="node.status" /><Badge v-if="node.reason" tone="warn">{{ node.reason }}</Badge><span class="mono text-xs ml-auto">{{ eur(data.costs?.[node.id], 4) }}</span></div>
          <div><div class="text-xs muted">Ziel</div><p>{{ node.contract.goal }}</p></div>
          <div v-if="node.contract.acceptance?.length"><div class="text-xs muted">Akzeptanzkriterien</div><ul class="list-disc ml-5"><li v-for="a in node.contract.acceptance" :key="a">{{ a }}</li></ul></div>
          <div><div class="text-xs muted">Tool-Scope</div><div class="flex flex-wrap gap-1 mt-1"><Badge v-for="t in node.contract.tool_scope || []" :key="t" tone="info">{{ t }}</Badge><span v-if="!(node.contract.tool_scope || []).length" class="muted text-xs">keine Tools</span></div></div>
          <div class="text-xs muted">Budget: <span class="mono">{{ node.contract.budget?.tokens || '—' }} Tokens · {{ node.contract.budget?.wall_clock_s || '—' }} s</span></div>
          <details><summary class="text-xs muted cursor-pointer">Ausgabe-Schema</summary><pre class="mono text-[11px] whitespace-pre-wrap">{{ JSON.stringify(node.contract.output_schema, null, 2) }}</pre></details>
          <div v-if="node.result"><div class="text-xs muted">Ergebnis (untrusted, schema-validiert)</div><pre class="mono text-[11px] whitespace-pre-wrap panel-2 rounded p-2 max-h-60 overflow-auto">{{ JSON.stringify(node.result, null, 2) }}</pre></div>
          <div v-if="node.notes" class="text-xs"><span class="muted">Notizen:</span> {{ node.notes }}</div>
          <RouterLink v-if="node.run_id" :to="`/dots/${node.owner_dot_id || g.lead_dot_id}`" class="text-xs hover:underline">Lauf {{ short(node.run_id) }} ansehen →</RouterLink>
          <div class="border-t line pt-3 space-y-2">
            <form v-if="node.status === 'running'" class="flex gap-2" @submit.prevent="act(`/nodes/${node.id}/steer`, { text: steerText }); steerText = ''"><input v-model="steerText" class="input" placeholder="Hinweis injizieren (Steer)" /><Btn type="submit" size="sm">Senden</Btn></form>
            <div class="flex flex-wrap gap-2">
              <Btn v-if="node.status === 'failed'" size="sm" @click="act(`/nodes/${node.id}/retry`)">Retry</Btn>
              <Btn v-if="['failed', 'blocked', 'ready', 'running'].includes(node.status)" size="sm" variant="ghost" @click="act(`/nodes/${node.id}/reassign`, { owner_kind: 'lead', dot_id: g.lead_dot_id, tier: 'planner' })">Lead übernimmt</Btn>
              <Btn v-if="['failed', 'blocked'].includes(node.status)" size="sm" variant="ghost" @click="act(`/nodes/${node.id}/reassign`, { owner_kind: 'worker', dot_id: '', tier: 'planner' })">Worker, stärkeres Modell</Btn>
              <Btn v-if="!['done', 'cancelled'].includes(node.status)" size="sm" variant="danger" @click="act(`/nodes/${node.id}/cancel`)">Abbrechen</Btn>
            </div>
            <form v-if="node.owner_kind === 'human' && ['ready', 'running'].includes(node.status)" class="space-y-2" @submit.prevent="act(`/nodes/${node.id}/complete`, { result: JSON.parse(humanResult) })">
              <div class="text-xs muted">Deine Entscheidung (JSON gemäß Schema)</div><textarea v-model="humanResult" class="input mono text-xs" rows="4" /><Btn type="submit" size="sm">Abschließen (Take-over)</Btn>
            </form>
          </div>
          <p class="text-[11px] muted">Aktualisiert {{ dt(node.updated_at) }}</p>
        </div>
        <p v-else class="text-sm muted">Klicke auf einen Knoten für Vertrag, Live-Status, Ergebnis und Aktionen (Steer, Cancel, Reassign, Take-over, Retry).</p>
      </Card>
    </div>
    <Card v-else title="Timeline" subtitle="Wer hat wann woran gearbeitet – Wartezeiten und Parallelität"><Chart v-if="timeline" :option="timeline" height="420px" /></Card>
  </div>
</template>
