<script setup lang="ts">
import { ref, onMounted, nextTick, computed } from 'vue'
import { get, post, patch, put, del } from '@/lib/api'
import { subscribe } from '@/lib/sse'
import { dt, eur, ago, short } from '@/lib/format'
import { useToast } from '@/stores/toast'
import { useAuth } from '@/stores/auth'
import Tabs from '@/components/Tabs.vue'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Status from '@/components/Status.vue'
import Badge from '@/components/Badge.vue'
import Journal from '@/components/Journal.vue'
import MemoryBrowser from '@/components/MemoryBrowser.vue'
import Empty from '@/components/Empty.vue'
import Emblem from '@/components/Emblem.vue'
import ChatPanel from '@/components/ChatPanel.vue'
const props = defineProps<{ id: string }>()
const toast = useToast()
const auth = useAuth()
const tab = ref('chat')
const view = ref<any>(null)
const dot = computed(() => view.value?.dot)
const tabs = computed(() => [
  { id: 'chat', label: 'Chat' }, { id: 'activity', label: 'Activity' }, { id: 'memory', label: 'Memory' },
  { id: 'channels', label: 'Kanäle & Link' }, { id: 'settings', label: 'Einstellungen' },
])
async function loadDot() { view.value = await get(`/dots/${props.id}`) }

// ---- Chat ----
const messages = ref<any[]>([])
const input = ref('')
const live = ref('')
const liveRun = ref<string | null>(null)
const sending = ref(false)
const box = ref<HTMLElement>()
let closeRun: (() => void) | null = null
async function loadMessages() {
  const r = await get(`/dots/${props.id}/messages`)
  messages.value = r.messages
  await nextTick(); box.value?.scrollTo({ top: box.value.scrollHeight })
}
async function send() {
  const text = input.value.trim()
  if (!text) return
  sending.value = true
  try {
    const r = await post(`/dots/${props.id}/chat`, { text })
    input.value = ''
    messages.value.push({ role: 'user', text, created_at: new Date().toISOString() })
    if (!r.steered) watchRun(r.run_id)
    await nextTick(); box.value?.scrollTo({ top: box.value.scrollHeight })
  } catch (e) { toast.err(e) } finally { sending.value = false }
}
function watchRun(id: string) {
  closeRun?.(); live.value = ''; liveRun.value = id
  closeRun = subscribe([`run.${id}`], (_t, d) => {
    if (d.type === 'delta') { live.value += d.text; box.value?.scrollTo({ top: box.value.scrollHeight }) }
    if (d.type === 'final' || (d.type === 'status' && ['succeeded', 'failed', 'cancelled', 'waiting'].includes(d.status))) {
      if (d.type === 'status' && d.status === 'waiting') toast.push('Wartet auf deine Freigabe (Regeln & Approvals)', 'info')
      if (d.type === 'status' && d.status === 'failed') toast.push('Lauf fehlgeschlagen: ' + (d.error || ''), 'err')
      live.value = ''; liveRun.value = null; closeRun?.(); loadMessages()
    }
  })
}
async function stop() { if (liveRun.value) await post(`/runs/${liveRun.value}/cancel`).catch(toast.err) }

// ---- Activity ----
const runs = ref<any[]>([])
const selected = ref<any>(null)
async function loadRuns() { runs.value = await get(`/dots/${props.id}/runs?limit=60`) }
async function openRun(r: any) { selected.value = await get(`/runs/${r.id}`) }

// ---- Kanäle & Link ----
const pairing = ref<any>(null)
const links = ref<any[]>([])
const newLink = ref<any>(null)
const channels = ref<any>({})
async function loadChannels() { [links.value, channels.value] = await Promise.all([get(`/dots/${props.id}/links`), get('/channels')]) }
async function pair() { try { pairing.value = await post(`/dots/${props.id}/pair`) } catch (e) { toast.err(e) } }
async function createLink() { try { newLink.value = await post(`/dots/${props.id}/links`, { name: 'Laptop' }); loadChannels() } catch (e) { toast.err(e) } }
async function revoke(l: any) { try { await del(`/links/${l.id}`); toast.ok('Link getrennt'); loadChannels() } catch (e) { toast.err(e) } }

// ---- Einstellungen ----
const s = ref<any>({})
const budget = ref(0)
function initSettings() {
  const d = dot.value
  const pc = d.pulse_config || {}
  s.value = { name: d.name, persona: d.persona, charter: d.charter, autonomy_level: d.autonomy_level, privacy_mode: d.privacy_mode, status: d.status,
    pulse: { interval_min: pc.interval_min ?? 30, top_k: pc.top_k ?? 5, max_nudges_per_day: pc.max_nudges_per_day ?? 6, digest_times: (pc.digest_times || ['08:00', '18:00']).join(', '),
      feeds: (pc.feeds || []).join('\n'), interests: (pc.interests || []).join(', ') },
    quiet: { start: d.quiet_hours?.start || '22:00', end: d.quiet_hours?.end || '07:00' }, tiers: { ...(d.tiers || {}) } }
}
const split = (x: string, sep: RegExp) => x.split(sep).map((v) => v.trim()).filter(Boolean)
async function saveSettings() {
  const v = s.value
  try {
    await patch(`/dots/${props.id}`, { name: v.name, persona: v.persona, charter: v.charter, autonomy_level: Number(v.autonomy_level), privacy_mode: v.privacy_mode, status: v.status,
      pulse_config: { interval_min: Number(v.pulse.interval_min), top_k: Number(v.pulse.top_k), max_nudges_per_day: Number(v.pulse.max_nudges_per_day),
        digest_times: split(v.pulse.digest_times, /,/), feeds: split(v.pulse.feeds, /\n/), interests: split(v.pulse.interests, /,/) },
      quiet_hours: v.quiet, tiers: Object.fromEntries(Object.entries(v.tiers).filter(([, m]) => m)) })
    if (budget.value > 0) await put('/budgets', { scope: 'dot', scope_id: props.id, period: 'day', limit_micro_eur: Math.round(budget.value * 1e6), hard: true })
    toast.ok('Gespeichert'); await loadDot(); initSettings()
  } catch (e) { toast.err(e) }
}
async function pulseNow() { try { const r = await post(`/pulse/${props.id}/run`); toast.ok(r.run_id ? 'Pulse gestartet' : 'Keine neuen Signale') } catch (e) { toast.err(e) } }

onMounted(async () => {
  try { await loadDot(); initSettings(); await Promise.all([loadMessages(), loadRuns(), loadChannels()]) } catch (e) { toast.err(e) }
})
const autonomyText = ['L0 Vorsicht – jede Aktion wird gefragt', 'L1 Standard – Lesen/intern frei, extern fragen', 'L2 Vertraut – externe Änderungen auto-geprüft', 'L3 Autonom – nach Regeln, Kommunikation auto-geprüft']
</script>
<template>
  <div v-if="dot" class="space-y-4">
    <header class="panel panel-glow p-5 flex flex-wrap items-center gap-4">
      <div class="size-14 rounded-2xl flex items-center justify-center shrink-0" :class="view.running ? 'aura' : ''" style="background: linear-gradient(145deg, rgba(255,74,92,.18), rgba(200,32,47,.04)); box-shadow: inset 0 0 0 1px var(--accent-glow)"><Emblem :size="32" glow /></div>
      <div class="min-w-0">
        <div class="flex items-center gap-2.5 flex-wrap"><h1 class="text-2xl font-semibold tracking-tight">{{ dot.name }}</h1><Status :s="dot.status" /></div>
        <div class="flex flex-wrap gap-2 mt-2"><Badge tone="muted">{{ dot.kind }}</Badge><Badge tone="accent">Autonomie L{{ dot.autonomy_level }}</Badge><Badge tone="info">{{ dot.privacy_mode }}</Badge><Badge v-if="view.sandbox" tone="ok" icon="🖥">Computer {{ view.sandbox }}</Badge></div>
      </div>
      <div class="flex-1" />
      <div class="text-right mr-2"><div class="text-[11px] uppercase tracking-wider muted">Kosten heute</div><div class="mono text-xl">{{ eur(view.cost_today_micro_eur) }}</div></div>
      <div class="flex gap-2"><RouterLink :to="`/computer/${id}`"><Btn variant="ghost" size="sm">🖥 Computer</Btn></RouterLink><Btn variant="ghost" size="sm" @click="pulseNow">⚡ Pulse</Btn></div>
    </header>
    <Tabs v-model="tab" :tabs="tabs" />

    <div v-if="tab === 'chat'" class="panel overflow-hidden h-[calc(100vh-15.5rem)] min-h-[28rem]"><ChatPanel :dot="{ id, name: dot.name }" /></div>

    <div v-else-if="tab === 'activity'" class="grid gap-4 lg:grid-cols-5">
      <Card title="Läufe" class="lg:col-span-2" flush>
        <template #actions><Btn size="sm" variant="subtle" @click="loadRuns">↻</Btn></template>
        <ul class="divide-y divide-[var(--line)] max-h-[70vh] overflow-auto">
          <li v-for="r in runs" :key="r.id"><button class="w-full text-left px-4 py-2.5 hover:bg-[var(--panel-2)] focus-ring" :class="{ 'bg-[var(--panel-2)]': selected?.run?.id === r.id }" @click="openRun(r)">
            <div class="flex items-center gap-2 text-sm"><Status :s="r.status" /><span class="muted">{{ r.kind }}</span><Badge v-if="r.tainted" tone="err" icon="☣">tainted</Badge><span class="ml-auto text-xs muted">{{ ago(r.created_at) }}</span></div>
            <div class="text-xs muted truncate mt-0.5">{{ r.input?.text }}</div>
            <div class="text-[11px] muted mono">{{ short(r.id) }} · {{ r.tokens }} Tokens · {{ eur(r.cost_micro_eur, 4) }}</div>
          </button></li>
        </ul>
      </Card>
      <Card title="Journal" :subtitle="selected ? short(selected.run.id) : 'Lauf wählen'" class="lg:col-span-3">
        <Journal v-if="selected" :data="selected" /><Empty v-else>Wähle links einen Lauf, um Tool-Calls, Policy-Entscheidungen, Freigaben und Router-Entscheidungen zu sehen.</Empty>
      </Card>
    </div>

    <MemoryBrowser v-else-if="tab === 'memory'" :dot="id" />

    <div v-else-if="tab === 'channels'" class="grid gap-4 lg:grid-cols-2">
      <Card title="Discord & Telegram koppeln">
        <p class="text-sm muted mb-3">Erzeuge einen Code und sende ihn dem Bot per Direktnachricht. Nur gekoppelte Konten dürfen die Fylgja steuern.</p>
        <Btn @click="pair">Pairing-Code erzeugen</Btn>
        <div v-if="pairing" class="mt-4 panel panel-2 p-4 text-center">
          <div class="mono text-3xl tracking-widest accent">{{ pairing.code }}</div>
          <div class="text-xs muted mt-2">gültig bis {{ dt(pairing.expires_at) }}</div>
        </div>
        <div class="mt-4 text-xs space-y-1">
          <div v-for="(h, k) in channels" :key="k" class="flex gap-2"><Status :s="h.ok ? 'active' : 'failed'" /><span class="mono">{{ k }}</span><span class="muted">{{ h.detail }}</span></div>
          <div v-if="!Object.keys(channels).length" class="muted">Keine Bots konfiguriert (FYLGJA_TELEGRAM_TOKEN / FYLGJA_DISCORD_TOKEN).</div>
        </div>
      </Card>
      <Card title="Laptop-Link" subtitle="Optional · nur ausgehend · jede Aktion braucht Freigabe">
        <Btn variant="ghost" @click="createLink">Link-Zugang erzeugen</Btn>
        <div v-if="newLink" class="mt-3 panel panel-2 p-3 text-xs"><div class="muted mb-1">Auf dem Laptop ausführen (Token wird nur einmal angezeigt):</div><code class="mono break-all">{{ newLink.command }}</code></div>
        <ul class="mt-4 space-y-2 text-sm">
          <li v-for="l in links" :key="l.id" class="flex items-center gap-2">
            <Status :s="l.revoked_at ? 'cancelled' : l.online ? 'active' : 'stopped'" /><span>{{ l.name }}</span><span class="text-xs muted">zuletzt {{ ago(l.last_seen) }}</span>
            <div class="flex-1" /><Btn v-if="!l.revoked_at" size="sm" variant="danger" @click="revoke(l)">Trennen (Kill-Switch)</Btn>
          </li>
        </ul>
      </Card>
    </div>

    <form v-else-if="tab === 'settings'" class="grid gap-4 lg:grid-cols-2" @submit.prevent="saveSettings">
      <Card title="Identität">
        <div class="space-y-3">
          <div><label class="text-xs muted">Name</label><input v-model="s.name" class="input mt-1" /></div>
          <div><label class="text-xs muted">Persona</label><input v-model="s.persona" class="input mt-1" /></div>
          <div><label class="text-xs muted">Charter</label><textarea v-model="s.charter" rows="5" class="input mt-1" /></div>
          <div><label class="text-xs muted">Status</label><select v-model="s.status" class="input mt-1"><option value="active">aktiv</option><option value="paused">pausiert</option><option value="archived">archiviert</option></select></div>
        </div>
      </Card>
      <Card title="Kontrolle" subtitle="Mehr Autonomie oder weniger Privacy erfordert Step-up">
        <div class="space-y-3">
          <div><label class="text-xs muted">Autonomiestufe</label>
            <select v-model.number="s.autonomy_level" class="input mt-1" :disabled="!auth.can('manage')"><option v-for="(t, i) in autonomyText" :key="i" :value="i">{{ t }}</option></select></div>
          <div><label class="text-xs muted">Privacy-Routing</label>
            <select v-model="s.privacy_mode" class="input mt-1"><option value="self_hosted_only">Nur eigene Infrastruktur (RunPod/lokal)</option><option value="eu_only">Nur EU-Standorte</option><option value="any">Beliebig (externe APIs erlaubt)</option></select></div>
          <div><label class="text-xs muted">Tagesbudget (€, hart) – leer lassen = unverändert</label><input v-model.number="budget" type="number" min="0" step="0.5" class="input mt-1" /></div>
          <div class="grid grid-cols-2 gap-2">
            <div v-for="t in ['planner', 'worker', 'triage', 'reviewer']" :key="t"><label class="text-xs muted">Modell für {{ t }}</label><input v-model="s.tiers[t]" class="input mt-1 mono text-xs" placeholder="Standard" /></div>
          </div>
        </div>
      </Card>
      <Card title="Pulse (proaktiv, nur lesend)">
        <div class="grid grid-cols-3 gap-2">
          <div><label class="text-xs muted">Takt (min, 0 = aus)</label><input v-model.number="s.pulse.interval_min" type="number" min="0" class="input mt-1" /></div>
          <div><label class="text-xs muted">Top-K</label><input v-model.number="s.pulse.top_k" type="number" min="1" class="input mt-1" /></div>
          <div><label class="text-xs muted">Max. Hinweise/Tag</label><input v-model.number="s.pulse.max_nudges_per_day" type="number" min="1" class="input mt-1" /></div>
          <div class="col-span-3"><label class="text-xs muted">Digest-Zeiten</label><input v-model="s.pulse.digest_times" class="input mt-1" placeholder="08:00, 18:00" /></div>
          <div class="col-span-3"><label class="text-xs muted">Interessen (Scoring)</label><input v-model="s.pulse.interests" class="input mt-1" placeholder="go, postgres, hosting" /></div>
          <div class="col-span-3"><label class="text-xs muted">RSS/Atom-Feeds (eine URL pro Zeile)</label><textarea v-model="s.pulse.feeds" rows="3" class="input mt-1 mono text-xs" /></div>
        </div>
      </Card>
      <Card title="Ruhezeiten">
        <div class="grid grid-cols-2 gap-2">
          <div><label class="text-xs muted">Von</label><input v-model="s.quiet.start" type="time" class="input mt-1" /></div>
          <div><label class="text-xs muted">Bis</label><input v-model="s.quiet.end" type="time" class="input mt-1" /></div>
        </div>
        <p class="text-xs muted mt-2">Nur Dringlichkeit „kritisch“ durchbricht die Ruhezeit; alles andere landet im Digest.</p>
        <div class="flex justify-end mt-6"><Btn type="submit">Speichern</Btn></div>
      </Card>
    </form>
  </div>
</template>
