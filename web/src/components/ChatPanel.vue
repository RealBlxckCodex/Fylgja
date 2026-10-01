<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'
import { ArrowUp, Square, Copy, ThumbsUp, ThumbsDown, RefreshCw, Check, ChevronDown, ShieldCheck, Plus } from 'lucide-vue-next'
import { get, post } from '@/lib/api'
import { subscribe } from '@/lib/sse'
import { renderMarkdown } from '@/lib/markdown'
import { dt } from '@/lib/format'
import { useToast } from '@/stores/toast'
import { useAuth } from '@/stores/auth'
import Emblem from './Emblem.vue'
import ThinkingBlock, { type Step } from './ThinkingBlock.vue'
const props = defineProps<{ dot: { id: string; name: string } }>()
const toast = useToast()
const auth = useAuth()
interface Msg { id?: string; role: string; text: string; created_at?: string; trust?: string; run_id?: string; local?: boolean }
const messages = ref<Msg[]>([])
const info = ref<Record<string, { tools: string[]; duration_ms: number; tainted: boolean; model: string }>>({})
const fb = ref<Record<string, string>>({})
const input = ref('')
const tier = ref('worker')
const sending = ref(false)
const scroller = ref<HTMLElement>()
const ta = ref<HTMLTextAreaElement>()
const atBottom = ref(true)
// Live-Lauf
const liveRun = ref<string | null>(null)
const liveText = ref('')
const liveSteps = ref<Step[]>([])
const liveStart = ref(0)
const now = ref(Date.now())
const approvals = ref<any[]>([])
let ticker: number | undefined
let closeRun: (() => void) | null = null
const copied = ref<string | null>(null)
// Verlauf-Schritte (lazy)
const history = ref<Record<string, Step[]>>({})

function scrollDown(force = false) { nextTick(() => { const el = scroller.value; if (el && (force || atBottom.value)) el.scrollTo({ top: el.scrollHeight, behavior: force ? 'smooth' : 'auto' }) }) }
function onScroll() { const el = scroller.value!; atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 120 }
async function load() {
  const r = await get(`/dots/${props.dot.id}/messages`)
  messages.value = r.messages; info.value = r.steps || {}
  fb.value = Object.fromEntries((r.feedback || []).map((f: any) => [f.message_id, f.kind]))
  scrollDown(true)
}
onMounted(() => { load().catch(toast.err); ticker = window.setInterval(() => (now.value = Date.now()), 500) })
onBeforeUnmount(() => { closeRun?.(); clearInterval(ticker) })
watch(() => props.dot.id, () => load().catch(toast.err))

function autosize() { const el = ta.value; if (!el) return; el.style.height = 'auto'; el.style.height = Math.min(el.scrollHeight, 220) + 'px' }
async function send(text?: string) {
  const t = (text ?? input.value).trim()
  if (!t || liveRun.value) return
  sending.value = true
  try {
    const r = await post(`/dots/${props.dot.id}/chat`, { text: t, tier: tier.value })
    input.value = ''; autosize()
    messages.value.push({ role: 'user', text: t, created_at: new Date().toISOString(), local: true })
    if (!r.steered) watchRun(r.run_id)
    scrollDown(true)
  } catch (e) { toast.err(e) } finally { sending.value = false }
}
function watchRun(id: string) {
  closeRun?.()
  liveRun.value = id; liveText.value = ''; liveSteps.value = []; liveStart.value = Date.now(); approvals.value = []
  closeRun = subscribe([`run.${id}`, 'approvals'], (topic, d) => {
    if (topic === 'approvals') { if (d.approval?.run_id === id) loadApprovals(id); return }
    if (d.type === 'delta') { liveText.value += d.text; scrollDown() }
    else if (d.type === 'step') applyStep(d)
    else if (d.type === 'final' || (d.type === 'status' && ['succeeded', 'failed', 'cancelled'].includes(d.status))) {
      if (d.type === 'status' && d.status === 'failed') toast.push('Lauf fehlgeschlagen: ' + (d.error || ''), 'err')
      finishRun()
    } else if (d.type === 'status' && d.status === 'waiting') loadApprovals(id)
  })
}
function finishRun() {
  closeRun?.(); closeRun = null
  const keep = liveSteps.value, dur = Date.now() - liveStart.value, rid = liveRun.value!
  liveRun.value = null; liveText.value = ''; approvals.value = []
  load().then(() => { const last = [...messages.value].reverse().find((m) => m.role === 'assistant'); if (last?.id) history.value[last.id] = keep.map((s) => ({ ...s, status: s.status === 'running' ? 'ok' : s.status })); if (last?.id && info.value[last.id]) info.value[last.id].duration_ms ||= dur }).catch(() => {})
  void rid
}
function applyStep(d: any) {
  const steps = liveSteps.value
  const closeThinking = () => { const t = [...steps].reverse().find((s) => s.kind === 'thinking' && !s.end); if (t) t.end = Date.now() }
  switch (d.step) {
    case 'thinking': steps.push({ id: 'th' + steps.length, kind: 'thinking', at: Date.now(), text: d.n > 1 ? 'Wertet die Ergebnisse aus …' : 'Überlegt, wie am besten vorzugehen ist …' }); break
    case 'reasoning': closeThinking(); steps.push({ id: 'rs' + steps.length, kind: 'reasoning', text: d.text, at: Date.now() }); break
    case 'tool_call': closeThinking(); steps.push({ id: d.call_id, kind: 'tool', tool: d.tool, cls: d.class, args: d.args, status: 'running', at: Date.now() }); break
    case 'policy': {
      const t = steps.find((s) => s.id === d.call_id)
      if (t && d.verdict !== 'allow') { steps.push({ id: 'po' + d.call_id, kind: 'policy', verdict: d.verdict, reasons: d.reasons, at: Date.now() }); if (d.verdict === 'ask' || d.verdict === 'review') t.status = 'ask'; if (d.verdict === 'deny') t.status = 'denied' }
      break
    }
    case 'tool_result': { const t = steps.find((s) => s.id === d.call_id); if (t) { t.status = d.is_error ? 'err' : 'ok'; t.preview = d.preview; t.untrusted = d.untrusted; t.end = Date.now() } break }
  }
  // Thinking-Zeilen ohne Folgeinhalt entfernen, damit der Verlauf aufgeräumt bleibt.
  for (let i = steps.length - 2; i >= 0; i--) if (steps[i].kind === 'thinking' && steps[i].end) steps.splice(i, 1)
  scrollDown()
}
async function loadApprovals(run: string) { try { approvals.value = (await get('/approvals?status=pending')).filter((a: any) => a.run_id === run) } catch { /* */ } }
async function resolve(a: any, approve: boolean) {
  try { await post(`/approvals/${a.id}/resolve`, { approve }); approvals.value = approvals.value.filter((x) => x.id !== a.id); toast.ok(approve ? 'Freigegeben' : 'Abgelehnt') } catch (e) { toast.err(e) }
}
async function stop() { if (liveRun.value) await post(`/runs/${liveRun.value}/cancel`).catch(toast.err) }

// Schritte vergangener Antworten lazy aus dem Journal
async function loadHistory(m: Msg) {
  if (!m.id || history.value[m.id] || !m.run_id) return
  const r = await get(`/runs/${m.run_id}`)
  const steps: Step[] = []
  for (const e of r.events) {
    const p = e.payload || {}
    if (e.type === 'tool_call') steps.push({ id: p.call_id, kind: 'tool', tool: p.tool, cls: p.class, args: tryJSON(p.args), status: 'ok', at: 0 })
    else if (e.type === 'policy' && p.decision?.verdict !== 'allow') steps.push({ id: 'po' + p.call_id, kind: 'policy', verdict: p.decision?.verdict, reasons: p.decision?.reasons, at: 0 })
    else if (e.type === 'tool_result') { const t = steps.find((s) => s.id === p.call_id); if (t) { t.status = p.is_error ? 'err' : 'ok'; t.preview = p.content?.slice(0, 600); t.untrusted = p.untrusted } }
    else if (e.type === 'model_response' && p.reasoning) steps.push({ id: 'rs' + e.seq, kind: 'reasoning', text: p.reasoning, at: 0 })
  }
  history.value[m.id] = steps
}
watch(messages, (ms) => { for (const m of ms) if (m.role !== 'user') loadHistory(m).catch(() => {}) }, { deep: true, immediate: true })
const tryJSON = (x: any) => { if (typeof x !== 'string') return x; try { return JSON.parse(x) } catch { return x } }

async function copy(m: Msg) { try { await navigator.clipboard.writeText(m.text); copied.value = m.id || ''; setTimeout(() => (copied.value = null), 1500) } catch { toast.push('Kopieren nicht möglich', 'err') } }
async function rate(m: Msg, kind: 'thumb_up' | 'thumb_down') {
  if (!m.id) return
  const next = fb.value[m.id] === kind ? 'none' : kind
  try { await post(`/messages/${m.id}/feedback`, { kind: next }); if (next === 'none') delete fb.value[m.id]; else fb.value[m.id] = next; if (next === 'thumb_down') toast.push('Danke – ich lerne daraus.', 'info') } catch (e) { toast.err(e) }
}
function regenerate(m: Msg) { const i = messages.value.indexOf(m); const prev = [...messages.value.slice(0, i)].reverse().find((x) => x.role === 'user'); if (prev) send(prev.text) }
function onClick(e: MouseEvent) {
  const b = (e.target as HTMLElement).closest('[data-copy]') as HTMLElement | null
  if (!b) return
  const code = b.closest('.md-code')?.querySelector('code')?.textContent || ''
  navigator.clipboard?.writeText(code).then(() => { b.textContent = 'Kopiert ✓'; setTimeout(() => (b.textContent = 'Kopieren'), 1400) })
}
const suggestions = computed(() => [
  { t: 'Was steht heute an?', s: 'Überblick über Termine, Aufgaben und Freigaben' },
  { t: 'Fasse meine offenen Aufgaben zusammen', s: 'Mit Priorität und nächster Aktion' },
  { t: 'Recherchiere drei Hosting-Anbieter und vergleiche sie', s: 'Mit Quellen, Preisen und Empfehlung' },
  { t: 'Merk dir: Ich bevorzuge Meetings vormittags', s: 'Dauerhaft im Gedächtnis speichern' },
])
const greeting = computed(() => `Wie kann ich helfen, ${(auth.principal?.display_name || '').split(' ')[0]}?`)
const elapsed = computed(() => (liveRun.value ? now.value - liveStart.value : 0))
const rendered = (t: string) => renderMarkdown(t)
const canSend = computed(() => input.value.trim().length > 0 && !liveRun.value)
</script>
<template>
  <div class="relative flex flex-col h-full min-h-0">
    <div ref="scroller" class="flex-1 overflow-y-auto" @scroll="onScroll" @click="onClick">
      <div class="mx-auto w-full max-w-3xl px-4 md:px-6 py-8">
        <!-- Leerzustand -->
        <div v-if="!messages.length && !liveRun" class="pt-[8vh] pb-8 flex flex-col items-center text-center rise">
          <div class="size-14 rounded-2xl flex items-center justify-center mb-6" style="background: linear-gradient(145deg, rgba(255,74,92,.2), rgba(200,32,47,.05)); box-shadow: inset 0 0 0 1px var(--accent-glow)"><Emblem :size="32" glow /></div>
          <h2 class="text-3xl font-semibold tracking-tight text-gradient">{{ greeting }}</h2>
          <p class="muted mt-2 max-w-md">{{ dot.name }} arbeitet mit Gedächtnis, Werkzeugen und eigenem Computer – und fragt dich bei allem Riskanten vorher.</p>
          <div class="grid sm:grid-cols-2 gap-3 mt-10 w-full text-left">
            <button v-for="s in suggestions" :key="s.t" class="panel p-4 hover:-translate-y-0.5 hover:border-[var(--line-strong)] transition-all focus-ring" @click="send(s.t)"><div class="text-sm font-medium">{{ s.t }}</div><div class="text-xs muted mt-1">{{ s.s }}</div></button>
          </div>
        </div>

        <div class="space-y-9">
          <template v-for="(m, i) in messages" :key="m.id || i">
            <!-- Nutzer -->
            <div v-if="m.role === 'user'" class="flex justify-end rise">
              <div class="max-w-[85%] rounded-2xl rounded-br-md px-4 py-2.5 text-[15px] leading-relaxed md" style="background: var(--panel-2); box-shadow: inset 0 0 0 1px var(--line-strong)" v-html="rendered(m.text)" />
            </div>
            <!-- Assistent -->
            <div v-else class="group flex gap-4 rise">
              <div class="size-8 rounded-xl flex items-center justify-center shrink-0 mt-0.5" style="background: var(--panel-2); box-shadow: inset 0 0 0 1px var(--line)"><Emblem :size="18" /></div>
              <div class="min-w-0 flex-1">
                <ThinkingBlock v-if="m.id && (info[m.id]?.tools.length || history[m.id]?.length)" :steps="history[m.id] || (info[m.id].tools.map((t, k) => ({ id: 'x' + k, kind: 'tool', tool: t, status: 'ok', at: 0 })) as any)" :elapsed-ms="info[m.id]?.duration_ms" />
                <div class="md" v-html="rendered(m.text)" />
                <div class="flex items-center gap-0.5 mt-2.5 -ml-1.5 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity">
                  <button class="p-1.5 rounded-lg muted hover:text-[var(--text)] hover:bg-[var(--panel-2)] focus-ring" aria-label="Kopieren" @click="copy(m)"><Check v-if="copied === m.id" class="size-4" style="color: var(--ok)" /><Copy v-else class="size-4" /></button>
                  <button class="p-1.5 rounded-lg hover:bg-[var(--panel-2)] focus-ring" :class="fb[m.id || ''] === 'thumb_up' ? 'accent' : 'muted hover:text-[var(--text)]'" aria-label="Gute Antwort" @click="rate(m, 'thumb_up')"><ThumbsUp class="size-4" /></button>
                  <button class="p-1.5 rounded-lg hover:bg-[var(--panel-2)] focus-ring" :class="fb[m.id || ''] === 'thumb_down' ? 'accent' : 'muted hover:text-[var(--text)]'" aria-label="Schlechte Antwort" @click="rate(m, 'thumb_down')"><ThumbsDown class="size-4" /></button>
                  <button v-if="i === messages.length - 1" class="p-1.5 rounded-lg muted hover:text-[var(--text)] hover:bg-[var(--panel-2)] focus-ring" aria-label="Neu generieren" @click="regenerate(m)"><RefreshCw class="size-4" /></button>
                  <span class="text-[11px] muted ml-2">{{ dt(m.created_at) }}<span v-if="m.id && info[m.id]?.model"> · {{ info[m.id].model }}</span></span>
                </div>
              </div>
            </div>
          </template>

          <!-- Live-Antwort -->
          <div v-if="liveRun" class="flex gap-4 rise">
            <div class="size-8 rounded-xl flex items-center justify-center shrink-0 mt-0.5 aura" style="background: var(--panel-2); box-shadow: inset 0 0 0 1px var(--accent-glow)"><Emblem :size="18" /></div>
            <div class="min-w-0 flex-1">
              <ThinkingBlock :steps="liveSteps" :live="!liveText || liveSteps.some((s) => s.status === 'running')" :elapsed-ms="elapsed" />
              <div v-if="liveText" class="md mt-1" v-html="rendered(liveText)" /><span v-if="liveText" class="inline-block w-2 h-4 align-middle ml-0.5 animate-pulse" style="background: var(--accent-2)" />
              <div v-for="a in approvals" :key="a.id" class="mt-4 rounded-2xl p-4 panel-glow relative" style="background: var(--panel-2)">
                <div class="flex items-center gap-2 text-sm font-medium"><ShieldCheck class="size-4" style="color: var(--warn)" />Freigabe nötig <span class="text-xs muted font-normal">· {{ a.class }} · Risiko {{ a.risk }}</span></div>
                <p class="text-sm mt-2">{{ a.preview?.summary || a.tool }}</p><p class="text-xs muted mt-1">{{ a.reason }}</p>
                <div class="flex gap-2 mt-3"><button class="bg-accent text-white text-sm font-medium px-4 py-2 rounded-xl focus-ring" @click="resolve(a, true)">Freigeben</button><button class="text-sm px-4 py-2 rounded-xl border border-[var(--line-strong)] hover:bg-[var(--panel-2)] focus-ring" @click="resolve(a, false)">Ablehnen</button></div>
              </div>
            </div>
          </div>
        </div>
        <div class="h-6" />
      </div>
    </div>

    <button v-if="!atBottom" class="absolute left-1/2 -translate-x-1/2 bottom-[148px] size-9 rounded-full panel flex items-center justify-center hover:-translate-y-0.5 transition-transform focus-ring" aria-label="Nach unten" @click="scrollDown(true)"><ChevronDown class="size-4" /></button>

    <!-- Composer -->
    <div class="px-4 md:px-6 pb-4 pt-1">
      <form class="mx-auto max-w-3xl" @submit.prevent="send()">
        <div class="rounded-[22px] p-2.5 transition-shadow focus-within:shadow-[0_0_0_3px_var(--accent-glow)]" style="background: var(--panel-solid); box-shadow: inset 0 0 0 1px var(--line-strong), 0 12px 40px -16px rgba(0,0,0,.8)">
          <label for="chat-input" class="sr-only">Nachricht</label>
          <textarea id="chat-input" ref="ta" v-model="input" rows="1" class="w-full bg-transparent outline-none resize-none px-3 pt-2 pb-1 text-[15px] leading-relaxed max-h-[220px]" :placeholder="`Nachricht an ${dot.name} …`"
            @input="autosize" @keydown.enter.exact.prevent="send()" />
          <div class="flex items-center gap-2 pt-1">
            <div class="flex items-center gap-1 rounded-full px-1 py-0.5 text-xs" style="background: var(--panel-2)" role="group" aria-label="Modell-Stufe">
              <button v-for="t in [['worker', 'Standard'], ['planner', 'Gründlich'], ['triage', 'Schnell']]" :key="t[0]" type="button" class="px-2.5 py-1 rounded-full transition-colors focus-ring" :class="tier === t[0] ? 'text-[var(--text)]' : 'muted hover:text-[var(--text)]'"
                :style="tier === t[0] ? 'background: var(--panel-solid); box-shadow: inset 0 0 0 1px var(--line-strong)' : ''" @click="tier = t[0]">{{ t[1] }}</button>
            </div>
            <span class="text-[11px] muted hidden sm:inline">↵ senden · ⇧↵ Zeilenumbruch</span>
            <div class="flex-1" />
            <button v-if="liveRun" type="button" class="size-9 rounded-full flex items-center justify-center bg-[var(--text)] text-[var(--bg)] focus-ring" aria-label="Stopp" @click="stop"><Square class="size-3.5 fill-current" /></button>
            <button v-else type="submit" class="size-9 rounded-full flex items-center justify-center transition-all focus-ring" :class="canSend ? 'bg-accent text-white' : 'opacity-40'" :disabled="!canSend" aria-label="Senden"><ArrowUp class="size-[18px]" /></button>
          </div>
        </div>
        <p class="text-center text-[11px] muted mt-2.5">{{ dot.name }} kann Fehler machen. Riskante Aktionen brauchen immer deine Freigabe.</p>
      </form>
    </div>
  </div>
</template>
