<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { get, post } from '@/lib/api'
import { useStream } from '@/lib/sse'
import { ago, eur, short, ms, pct } from '@/lib/format'
import { useToast } from '@/stores/toast'
import { useAuth } from '@/stores/auth'
import Card from '@/components/Card.vue'
import Status from '@/components/Status.vue'
import Badge from '@/components/Badge.vue'
import Btn from '@/components/Btn.vue'
import Empty from '@/components/Empty.vue'
import Stat from '@/components/Stat.vue'
import Gauge from '@/components/Gauge.vue'
import Emblem from '@/components/Emblem.vue'
const toast = useToast()
const auth = useAuth()
const dots = ref<any[]>([]), approvals = ref<any[]>([]), proposals = ref<any[]>([]), graphs = ref<any[]>([]), fleet = ref<any>(null), usage = ref<any>(null)
const activity = ref<any[]>([])
const loaded = ref(false)
async function load() {
  const [d, a, p, g, f, u] = await Promise.all([get('/dots'), get('/approvals?status=pending'), get('/proposals'), get('/graphs'), get('/fleet/overview'), get('/usage')])
  dots.value = d; approvals.value = a; proposals.value = p.proposals; graphs.value = g.filter((x: any) => ['running', 'planned'].includes(x.status)); fleet.value = f; usage.value = u; loaded.value = true
}
onMounted(() => load().catch(toast.err))
useStream(() => [...dots.value.map((d) => `dot.${d.dot.id}.activity`), 'approvals', 'proposals'], (_t, d) => {
  if (d?.run) { activity.value.unshift({ at: new Date().toISOString(), type: d.type, run: d.run }); activity.value = activity.value.slice(0, 40) }
  load().catch(() => {})
})
async function resolve(a: any, approve: boolean) { try { await post(`/approvals/${a.id}/resolve`, { approve }); toast.ok(approve ? 'Freigegeben' : 'Abgelehnt'); load() } catch (e) { toast.err(e) } }
const greet = computed(() => { const h = new Date().getHours(); return h < 5 ? 'Noch wach' : h < 11 ? 'Guten Morgen' : h < 18 ? 'Guten Tag' : 'Guten Abend' })
const s = computed(() => fleet.value?.summary ?? {})
const agents = computed(() => fleet.value?.agents ?? [])
const daily = computed(() => (usage.value?.daily ?? []).map((d: any) => d.cost_micro_eur))
const lead = computed(() => dots.value.find((d) => d.dot.kind === 'personal') ?? dots.value[0])
const state = computed(() => (agents.value.length ? `${agents.value.length} Läufe aktiv` : approvals.value.length ? 'wartet auf dich' : 'alles ruhig'))
</script>
<template>
  <div class="space-y-6 stagger">
    <section class="panel panel-glow relative overflow-hidden p-6 md:p-8">
      <div class="absolute -right-10 -top-16 opacity-[.07] pointer-events-none"><Emblem :size="340" /></div>
      <div class="relative flex flex-wrap items-center gap-6">
        <div class="relative">
          <div class="size-16 rounded-2xl flex items-center justify-center" :class="agents.length ? 'aura' : ''" style="background: linear-gradient(145deg, rgba(255,74,92,.18), rgba(200,32,47,.05)); box-shadow: inset 0 0 0 1px var(--accent-glow)"><Emblem :size="38" glow /></div>
          <span class="absolute -bottom-1 -right-1 size-3.5 rounded-full border-2" :style="{ background: agents.length ? 'var(--info)' : 'var(--ok)', borderColor: 'var(--panel-solid)' }" />
        </div>
        <div class="flex-1 min-w-60">
          <div class="text-sm muted">{{ greet }}, {{ (auth.principal?.display_name || '').split(' ')[0] }}</div>
          <h1 class="text-3xl md:text-4xl font-semibold tracking-tight mt-1"><span class="text-gradient">{{ lead?.dot.name ?? 'Fylgja' }}</span> <span class="muted font-normal text-2xl">· {{ state }}</span></h1>
          <p class="text-sm muted mt-2">{{ approvals.length }} Freigabe{{ approvals.length === 1 ? '' : 'n' }} · {{ proposals.length }} Vorschläge · {{ graphs.length }} laufende Pläne</p>
        </div>
        <div v-if="lead" class="flex gap-2"><RouterLink :to="`/dots/${lead.dot.id}`"><Btn>Chat öffnen</Btn></RouterLink><RouterLink to="/fleet"><Btn variant="ghost">Flotte</Btn></RouterLink></div>
      </div>
    </section>

    <div class="grid grid-cols-2 xl:grid-cols-4 gap-4">
      <Stat label="Kosten heute" :value="eur(s.cost_today_micro_eur)" :spark="daily" hint="Verlauf: Monat" to="/costs" />
      <Stat label="Aktive Läufe" :value="String(agents.length)" :hint="`${dots.length} Fylgjur`" to="/dots" />
      <Stat label="Queue-Wartezeit p95" :value="ms(s.queue_wait_p95_ms)" :tone="(s.queue_wait_p95_ms ?? 0) > 10000 ? 'warn' : undefined" to="/fleet" hint="Router" />
      <div class="panel p-4 flex items-center gap-4 hover:-translate-y-0.5 transition-transform"><Gauge :value="s.gpu_util ?? 0" :size="72" /><div><div class="text-[11px] uppercase tracking-wider muted">GPU-Last</div><div class="mono text-lg mt-1">{{ s.nodes_ready ?? 0 }}<span class="muted">/{{ s.nodes_total ?? 0 }} Nodes</span></div><div class="text-xs muted">{{ s.sandboxes_running ?? 0 }} Sandboxen</div></div></div>
    </div>

    <div class="grid gap-6 xl:grid-cols-3">
      <div class="xl:col-span-2 space-y-6">
        <Card title="Was läuft gerade" :subtitle="`${agents.length} aktive Läufe`" flush>
          <ul v-if="agents.length" class="divide-y divide-[var(--line)]">
            <li v-for="a in agents" :key="a.run_id" class="px-5 py-3.5 flex items-center gap-4">
              <Status :s="a.status" />
              <div class="min-w-0 flex-1"><div class="text-sm truncate"><RouterLink :to="`/dots/${a.dot_id}`" class="font-medium hover:underline">{{ a.dot_name }}</RouterLink> <span class="muted">· {{ a.kind }}</span></div><div class="text-xs muted truncate">{{ a.task }}</div></div>
              <div class="hidden md:block text-right text-xs mono muted">{{ Object.keys(a.deployments).join(', ') || '—' }}</div>
              <div class="mono text-sm w-20 text-right">{{ eur(a.cost_today_micro_eur) }}</div>
            </li>
          </ul>
          <Empty v-else>Gerade ist alles ruhig. Schreib deiner Fylgja – oder lass sie im Hintergrund arbeiten (Pulse).</Empty>
          <div v-if="graphs.length" class="border-t line px-5 py-3 space-y-2">
            <RouterLink v-for="g in graphs" :key="g.id" :to="`/teams/${g.id}`" class="flex items-center gap-3 text-sm hover:underline"><Status :s="g.status" /><span class="flex-1 truncate">{{ g.title || short(g.id) }}</span>
              <div class="h-1.5 w-28 rounded-full overflow-hidden" style="background: var(--panel-2)"><div class="h-full bg-accent" :style="{ width: (g.nodes ? (100 * g.done) / g.nodes : 0) + '%' }" /></div><span class="muted mono text-xs">{{ g.done }}/{{ g.nodes }}</span></RouterLink>
          </div>
        </Card>
        <Card title="Fylgjur" flush>
          <div class="grid sm:grid-cols-2 lg:grid-cols-3 gap-4 p-5">
            <RouterLink v-for="d in dots" :key="d.dot.id" :to="`/dots/${d.dot.id}`" class="group panel panel-2 p-4 block transition-all hover:-translate-y-0.5 hover:border-[var(--accent)]">
              <div class="flex items-center gap-3"><div class="size-10 rounded-xl flex items-center justify-center shrink-0" style="background: var(--panel-2); box-shadow: inset 0 0 0 1px var(--line)"><Emblem :size="22" /></div>
                <div class="min-w-0 flex-1"><div class="font-medium truncate">{{ d.dot.name }}</div><div class="text-[11px] muted">{{ d.dot.kind }} · L{{ d.dot.autonomy_level }}</div></div><Status :s="d.dot.status" /></div>
              <div class="flex items-center gap-4 text-xs mono mt-4 muted"><span title="Läufe">▶ {{ d.running }}</span><span title="wartend">⏸ {{ d.waiting }}</span><span title="Freigaben">🛡 {{ d.pending_approvals }}</span><span class="ml-auto text-[var(--text)]">{{ eur(d.cost_today_micro_eur) }}</span></div>
            </RouterLink>
          </div>
        </Card>
      </div>
      <div class="space-y-6">
        <Card title="Wartet auf dich" :subtitle="`${approvals.length} Freigaben · ${proposals.length} Vorschläge`" flush glow>
          <ul class="divide-y divide-[var(--line)]">
            <li v-for="a in approvals.slice(0, 6)" :key="a.id" class="p-4 space-y-2.5">
              <div class="flex items-center gap-2 text-sm"><Badge tone="warn" icon="🛡">{{ a.class }}</Badge><span class="font-medium truncate">{{ a.preview?.summary || a.tool }}</span></div>
              <p class="text-xs muted line-clamp-2">{{ a.reason }}</p>
              <div class="flex gap-2"><Btn size="sm" @click="resolve(a, true)">Freigeben</Btn><Btn size="sm" variant="ghost" @click="resolve(a, false)">Ablehnen</Btn><RouterLink :to="`/approvals/${a.id}`" class="text-xs muted self-center hover:underline ml-auto">Details</RouterLink></div>
            </li>
            <li v-for="p in proposals.slice(0, 4)" :key="p.id" class="px-4 py-3 text-sm"><RouterLink to="/learn" class="flex gap-2 items-center hover:underline"><Badge tone="info" icon="💡">{{ p.type }}</Badge><span class="truncate">{{ p.payload.name || p.payload.text || p.payload.new || 'Vorschlag' }}</span></RouterLink></li>
          </ul>
          <Empty v-if="!approvals.length && !proposals.length">Nichts offen.</Empty>
        </Card>
        <Card title="Live-Aktivität" subtitle="Diese Sitzung" flush>
          <ul class="text-sm divide-y divide-[var(--line)] max-h-80 overflow-auto">
            <li v-for="(e, i) in activity" :key="i" class="px-5 py-2.5 flex gap-2.5 items-center"><Status :s="e.run.status" /><span class="truncate flex-1 text-xs">{{ e.run.input?.text }}</span><span class="text-[11px] muted whitespace-nowrap">{{ ago(e.at) }}</span></li>
          </ul>
          <Empty v-if="!activity.length">Noch keine Aktivität.</Empty>
        </Card>
      </div>
    </div>
  </div>
</template>
