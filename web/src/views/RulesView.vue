<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { get, post, put, del } from '@/lib/api'
import { dt, short } from '@/lib/format'
import { useStream } from '@/lib/sse'
import { useToast } from '@/stores/toast'
import { useAuth } from '@/stores/auth'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Badge from '@/components/Badge.vue'
import Status from '@/components/Status.vue'
import Tabs from '@/components/Tabs.vue'
import Modal from '@/components/Modal.vue'
import Empty from '@/components/Empty.vue'
const props = defineProps<{ approval?: string }>()
const toast = useToast()
const auth = useAuth()
const tab = ref('approvals')
const approvals = ref<any[]>([])
const history = ref<any[]>([])
const rules = ref<any>({ rules: [], matrix: {}, classes: [] })
const dots = ref<any[]>([])
async function load() {
  const [p, h, r, d] = await Promise.all([get('/approvals?status=pending'), get('/approvals'), get('/rules'), get('/dots')])
  approvals.value = p; history.value = h.filter((x: any) => x.status !== 'pending'); rules.value = r; dots.value = d
}
onMounted(() => load().catch(toast.err))
useStream(() => ['approvals'], () => load().catch(() => {}))
const reason = ref<Record<string, string>>({})
async function resolve(a: any, approve: boolean) {
  try { await post(`/approvals/${a.id}/resolve`, { approve, reason: reason.value[a.id] || '' }); toast.ok(approve ? 'Freigegeben' : 'Abgelehnt'); load() } catch (e) { toast.err(e) }
}
const lowRisk = computed(() => approvals.value.filter((a) => a.risk === 'low' && !a.step_up))
async function bulk() { for (const a of lowRisk.value) await post(`/approvals/${a.id}/resolve`, { approve: true }).catch(toast.err); load() }
// Regel-Editor
const editOpen = ref(false)
const form = ref<any>({})
const templates = [
  { label: 'Mails nur an eigene Domain ohne Rückfrage', rule: { name: 'Interne Mails', effect: 'allow', expr: 'action.tool == "mail.send" && action.target.recipients.all(r, r.endsWith("@meinefirma.de"))' } },
  { label: 'Kurze Kalenderzusagen bekannter Kontakte', rule: { name: 'Kalender kurz', effect: 'allow', expr: 'action.tool == "calendar.respond" && action.args.response == "accept" && action.args.duration_minutes <= 60 && action.args.organizer in contacts.known' } },
  { label: 'rm -rf / immer blocken', rule: { name: 'rm -rf blocken', effect: 'deny', expr: 'action.tool.startsWith("shell.") && action.args.cmd.matches("(?i)rm\\\\s+-rf\\\\s+/")' } },
  { label: 'Alle externen Aktionen nachts fragen', rule: { name: 'Nachts fragen', effect: 'ask', expr: 'action.class in ["write_external","communicate"] && (ctx.time.getHours("Europe/Berlin") >= 22 || ctx.time.getHours("Europe/Berlin") < 7)' } },
]
function edit(r?: any) { form.value = r ? { ...r } : { name: '', expr: '', effect: 'ask', priority: 0, enabled: true, allow_when_tainted: false, four_eyes: false, dot_id: '' }; sim.value = null; editOpen.value = true }
async function save() {
  try { if (form.value.id) await put(`/rules/${form.value.id}`, strip(form.value)); else await post('/rules', strip(form.value)); editOpen.value = false; toast.ok('Regel gespeichert'); load() } catch (e) { toast.err(e) }
}
function strip(r: any) { const { id, version, created_at, ...rest } = r; return rest }
async function remove(r: any) { if (!confirm(`Regel „${r.name}“ löschen?`)) return; try { await del(`/rules/${r.id}`); load() } catch (e) { toast.err(e) } }
const sim = ref<any>(null)
async function simulate() {
  const others = rules.value.rules.filter((r: any) => r.id !== form.value.id)
  try { sim.value = await post('/rules/simulate', { rules: [...others, { ...form.value, id: form.value.id || 'neu', enabled: true }], days: 14 }) } catch (e) { toast.err(e) }
}
const verdictTone: Record<string, any> = { allow: 'ok', ask: 'warn', review: 'info', deny: 'err', human_only: 'accent' }
const levels = ['L0', 'L1', 'L2', 'L3']
</script>
<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">Regeln & Approvals</h1>
    <Tabs v-model="tab" :tabs="[{ id: 'approvals', label: 'Freigaben', badge: approvals.length || undefined }, { id: 'rules', label: 'Regeln' }, { id: 'matrix', label: 'Autonomie-Matrix' }, { id: 'history', label: 'Historie' }]" />
    <div v-if="tab === 'approvals'" class="space-y-3">
      <div v-if="lowRisk.length > 1" class="flex justify-end"><Btn size="sm" variant="ghost" @click="bulk">{{ lowRisk.length }} Low-Risk-Freigaben gesammelt erteilen</Btn></div>
      <Card v-for="a in approvals" :key="a.id" :title="a.preview?.summary || a.tool" :subtitle="`${a.tool} · läuft ab ${dt(a.expires_at)}`" :class="{ 'ring-1 ring-[var(--accent)]': approval === a.id }">
        <template #actions><Badge :tone="a.risk === 'high' ? 'err' : a.risk === 'medium' ? 'warn' : 'ok'">Risiko {{ a.risk }}</Badge><Badge tone="info">{{ a.class }}</Badge>
          <Badge v-if="a.required_approvals > 1" tone="accent">Vier-Augen {{ (a.approvals_given || []).length }}/{{ a.required_approvals }}</Badge><Badge v-if="a.step_up" tone="accent">🔐 Step-up</Badge></template>
        <div class="space-y-3 text-sm">
          <p class="muted">{{ a.reason }}</p>
          <blockquote v-if="a.preview?.rationale" class="border-l-2 line pl-3 text-xs muted whitespace-pre-wrap">{{ a.preview.rationale }}</blockquote>
          <pre class="mono text-xs panel-2 rounded p-3 whitespace-pre-wrap max-h-72 overflow-auto">{{ JSON.stringify(a.args_redacted, null, 2) }}</pre>
          <div class="flex flex-wrap gap-2 items-center">
            <Btn @click="resolve(a, true)">Freigeben</Btn>
            <input v-model="reason[a.id]" class="input !w-72" placeholder="Grund bei Ablehnung (optional)" />
            <Btn variant="ghost" @click="resolve(a, false)">Ablehnen</Btn>
          </div>
        </div>
      </Card>
      <Empty v-if="!approvals.length">Keine offenen Freigaben.</Empty>
    </div>
    <div v-else-if="tab === 'rules'" class="space-y-3">
      <div class="flex justify-between items-center"><p class="text-sm muted">Hierarchie: deny-Regel › human-only › Scope › Taint-Sperre › Custom Rules › Matrix. Regeln sind CEL-Ausdrücke.</p><Btn v-if="auth.can('manage')" @click="edit()">+ Regel</Btn></div>
      <Card flush>
        <table class="dense"><thead><tr><th>Name</th><th>Effekt</th><th>Ausdruck</th><th>Geltung</th><th>Prio</th><th>Flags</th><th /></tr></thead>
          <tbody><tr v-for="r in rules.rules" :key="r.id">
            <td>{{ r.name }} <span class="muted text-xs">v{{ r.version }}</span></td><td><Badge :tone="verdictTone[r.effect]">{{ r.effect }}</Badge></td>
            <td class="mono text-xs max-w-md break-all">{{ r.expr }}</td><td class="text-xs">{{ r.dot_id ? dots.find((d) => d.dot.id === r.dot_id)?.dot.name : 'alle' }}</td>
            <td class="mono">{{ r.priority }}</td>
            <td class="text-xs space-x-1"><Badge v-if="!r.enabled">aus</Badge><Badge v-if="r.allow_when_tainted" tone="err">auch tainted</Badge><Badge v-if="r.four_eyes" tone="accent">4 Augen</Badge></td>
            <td class="whitespace-nowrap"><Btn size="sm" variant="subtle" @click="edit(r)">Bearbeiten</Btn><Btn size="sm" variant="subtle" @click="remove(r)">Löschen</Btn></td></tr></tbody></table>
        <Empty v-if="!rules.rules.length">Keine eigenen Regeln – es gilt die Autonomie-Matrix.</Empty>
      </Card>
    </div>
    <Card v-else-if="tab === 'matrix'" title="Standard-Autonomie-Matrix" subtitle="Pro Fylgja über die Autonomiestufe wählbar; Custom Rules verfeinern" flush>
      <table class="dense"><thead><tr><th>Klasse</th><th v-for="l in levels" :key="l">{{ l }}</th></tr></thead>
        <tbody><tr v-for="c in rules.classes" :key="c"><td class="mono">{{ c }}</td><td v-for="(v, i) in rules.matrix[c]" :key="i"><Badge :tone="verdictTone[v]">{{ v }}</Badge><span v-if="c === 'spend'" class="text-[10px] muted ml-1">+Step-up</span></td></tr></tbody></table>
    </Card>
    <Card v-else title="Entschiedene Freigaben" flush>
      <table class="dense"><thead><tr><th>Zeit</th><th>Aktion</th><th>Klasse</th><th>Status</th><th>via</th></tr></thead>
        <tbody><tr v-for="a in history" :key="a.id"><td class="muted">{{ dt(a.resolved_at || a.created_at) }}</td><td>{{ a.preview?.summary || a.tool }}</td><td>{{ a.class }}</td><td><Status :s="a.status" /></td><td class="muted">{{ a.resolved_via || '—' }}</td></tr></tbody></table>
    </Card>
    <Modal :open="editOpen" :title="form.id ? 'Regel bearbeiten' : 'Neue Regel'" wide @close="editOpen = false">
      <div class="space-y-3 text-sm">
        <div v-if="!form.id" class="flex flex-wrap gap-2"><Btn v-for="t in templates" :key="t.label" size="sm" variant="ghost" @click="Object.assign(form, t.rule)">{{ t.label }}</Btn></div>
        <div class="grid grid-cols-3 gap-2">
          <input v-model="form.name" class="input col-span-2" placeholder="Name" />
          <select v-model="form.effect" class="input"><option value="allow">allow</option><option value="ask">ask</option><option value="deny">deny</option></select>
        </div>
        <textarea v-model="form.expr" class="input mono text-xs" rows="4" placeholder='action.tool == "mail.send" && …' />
        <p class="text-[11px] muted">Variablen: action.tool, action.class, action.args.*, action.target.domain, action.target.recipients, action.amount_micro_eur, ctx.autonomy, ctx.tainted, ctx.trigger, ctx.time, ctx.channel, user.role, history.approved_same_count, contacts.known</p>
        <div class="grid grid-cols-3 gap-2 items-center">
          <select v-model="form.dot_id" class="input"><option value="">Alle Fylgjur</option><option v-for="d in dots" :key="d.dot.id" :value="d.dot.id">{{ d.dot.name }}</option></select>
          <label class="flex gap-2 items-center">Priorität <input v-model.number="form.priority" type="number" class="input !w-20" /></label>
          <label class="flex gap-2 items-center"><input v-model="form.enabled" type="checkbox" /> aktiv</label>
          <label class="flex gap-2 items-center col-span-2"><input v-model="form.allow_when_tainted" type="checkbox" /> auch bei untrusted Kontext erlauben (Step-up)</label>
          <label class="flex gap-2 items-center"><input v-model="form.four_eyes" type="checkbox" /> Vier-Augen</label>
        </div>
        <div class="flex gap-2 justify-end"><Btn variant="ghost" @click="simulate">Simulieren (14 Tage)</Btn><Btn @click="save">Speichern</Btn></div>
        <div v-if="sim" class="border-t line pt-3">
          <p class="text-sm mb-2">Was wäre passiert? <b>{{ sim.changed }}</b> von {{ sim.total }} Aktionen würden anders entschieden.</p>
          <table class="dense"><thead><tr><th>Aktion</th><th>vorher</th><th>jetzt</th><th>Gründe</th></tr></thead>
            <tbody><tr v-for="x in sim.results.filter((r: any) => r.changed).slice(0, 30)" :key="x.id"><td class="mono text-xs">{{ x.tool }} <span class="muted">{{ short(x.id) }}</span></td>
              <td><Badge :tone="verdictTone[x.previous]">{{ x.previous }}</Badge></td><td><Badge :tone="verdictTone[x.now.verdict]">{{ x.now.verdict }}</Badge></td><td class="text-xs muted">{{ x.now.reasons.join('; ') }}</td></tr></tbody></table>
        </div>
      </div>
    </Modal>
  </div>
</template>
