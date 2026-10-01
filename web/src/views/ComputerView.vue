<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRouter } from 'vue-router'
import { get, post, del } from '@/lib/api'
import { subscribe } from '@/lib/sse'
import { ago } from '@/lib/format'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Empty from '@/components/Empty.vue'
import Status from '@/components/Status.vue'
import Badge from '@/components/Badge.vue'
import { Power, Moon, RotateCcw, Hand, Undo2, Download, Monitor, Terminal, Globe, FileText, MousePointer2 } from 'lucide-vue-next'

const props = defineProps<{ id?: string }>()
const router = useRouter()
const toast = useToast()
const dots = ref<any[]>([])
const session = ref<any>(null)
const busy = ref('')
const screen = ref<HTMLDivElement>()
const viewOnly = ref(true)
const feed = ref<{ at: string; tool: string; text: string }[]>([])
let rfb: any = null
let closeSse: (() => void) | undefined
let poll: number | undefined

const st = computed(() => session.value?.status ?? { state: 'none' })
const dot = computed(() => dots.value.find((d) => d.dot.id === props.id)?.dot)

async function load() {
  if (!props.id) return
  session.value = await get(`/dots/${props.id}/computer`).catch((e) => { toast.err(e); return null })
  await nextTickConnect()
}
async function nextTickConnect() {
  await nextTick()
  if (!session.value?.vnc || !screen.value) { rfb?.disconnect(); rfb = null; return }
  if (rfb) return
  const { default: RFB } = await import('@novnc/novnc')
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  rfb = new RFB(screen.value, `${proto}://${location.host}${session.value.vnc_path}`)
  rfb.viewOnly = viewOnly.value
  rfb.scaleViewport = true
}
async function act(name: string, fn: () => Promise<any>, ok: string) {
  busy.value = name
  try { await fn(); toast.ok(ok); rfb?.disconnect(); rfb = null; await load() } catch (e) { toast.err(e) } finally { busy.value = '' }
}
const wake = () => act('wake', () => post(`/dots/${props.id}/computer/wake`), 'Computer läuft')
const sleep = () => act('sleep', () => post(`/dots/${props.id}/computer/sleep`), 'Computer schläft (Dateien bleiben)')
const reset = () => { if (confirm('Computer neu aufsetzen? Installierte Programme und Prozesse gehen verloren, das Home-Verzeichnis bleibt.')) act('reset', () => del(`/dots/${props.id}/computer?keep_home=1`), 'Computer zurückgesetzt') }
function takeover() {
  viewOnly.value = !viewOnly.value
  if (rfb) rfb.viewOnly = viewOnly.value
  post(`/dots/${props.id}/computer/takeover`, { active: !viewOnly.value }).catch(() => {})
  toast.push(viewOnly.value ? 'Steuerung an die Fylgja zurückgegeben' : 'Du steuerst jetzt. Eingriffe werden protokolliert.', 'info')
}

const icon = (t: string) => t.startsWith('shell') ? Terminal : t.startsWith('browser') ? Globe : t.startsWith('fs') ? FileText : t.startsWith('desktop') ? MousePointer2 : Monitor
function describe(tool: string, a: any): string {
  a = a || {}
  switch (tool) {
    case 'shell.run': return '$ ' + (a.cmd ?? '')
    case 'fs.read': case 'fs.write': case 'fs.list': return a.path ?? '.'
    case 'browser.navigate': return a.url ?? ''
    case 'desktop.click': return `Klick (${a.x}, ${a.y})${a.double ? ' doppelt' : ''}${a.button && a.button !== 'left' ? ' ' + a.button : ''}`
    case 'desktop.drag': return `Ziehen (${a.x},${a.y}) → (${a.to_x},${a.to_y})`
    case 'desktop.type': return 'Tippt: ' + String(a.text ?? '').slice(0, 80)
    case 'desktop.key': return 'Taste: ' + (a.keys ?? '')
    case 'desktop.scroll': return 'Scrollt'
    case 'desktop.screenshot': return 'Schaut auf den Bildschirm'
    default: return Object.values(a).map(String).join(' ').slice(0, 80)
  }
}
function listen() {
  closeSse?.()
  feed.value = []
  if (!props.id) return
  closeSse = subscribe([`dot.${props.id}.activity`], (_t, d) => {
    if (d.type !== 'computer') return
    feed.value.unshift({ at: d.at, tool: d.tool, text: describe(d.tool, d.args) })
    if (feed.value.length > 60) feed.value.pop()
  })
}
const base = computed(() => `/api/v1/dots/${props.id}/computer`)

onMounted(async () => {
  dots.value = await get('/dots')
  if (!props.id && dots.value[0]) { router.replace(`/computer/${dots.value[0].dot.id}`); return }
  await load(); listen()
  poll = window.setInterval(() => { if (document.visibilityState === 'visible') load() }, 15000)
})
watch(() => props.id, async () => { rfb?.disconnect(); rfb = null; await load(); listen() })
onBeforeUnmount(() => { rfb?.disconnect(); closeSse?.(); clearInterval(poll) })
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center gap-3 flex-wrap">
      <h1 class="text-xl font-semibold">Computer</h1>
      <select class="input !w-56" :value="id" aria-label="Fylgja" @change="router.push(`/computer/${($event.target as HTMLSelectElement).value}`)"><option v-for="d in dots" :key="d.dot.id" :value="d.dot.id">{{ d.dot.name }}</option></select>
      <Status v-if="session?.available && st.state !== 'none'" :s="st.state === 'running' ? 'running' : st.state === 'sleeping' ? 'sleeping' : st.state === 'error' ? 'failed' : 'pending'" />
      <Badge v-else-if="session?.available" tone="muted">noch nicht gestartet</Badge>
      <Badge v-if="st.provider" tone="muted">{{ st.provider }}</Badge>
      <span v-if="st.last_active_at" class="text-xs muted">zuletzt aktiv {{ ago(st.last_active_at) }}</span>
      <div class="flex-1" />
      <template v-if="session?.available">
        <Btn v-if="st.state !== 'running'" size="sm" :disabled="!!busy" @click="wake"><Power class="size-4" /> Starten</Btn>
        <template v-else>
          <Btn size="sm" :variant="viewOnly ? 'primary' : 'danger'" :disabled="!session?.vnc" @click="takeover"><component :is="viewOnly ? Hand : Undo2" class="size-4" /> {{ viewOnly ? 'Übernehmen' : 'Zurückgeben' }}</Btn>
          <Btn size="sm" variant="ghost" :disabled="!!busy" @click="sleep"><Moon class="size-4" /> Schlafen</Btn>
        </template>
        <Btn size="sm" variant="ghost" :disabled="!!busy" @click="reset"><RotateCcw class="size-4" /> Neu aufsetzen</Btn>
      </template>
    </div>

    <div class="grid gap-4 xl:grid-cols-4">
      <Card :title="`Bildschirm von ${dot?.name ?? ''}`" :subtitle="viewOnly ? 'Du siehst zu. Die Fylgja arbeitet selbst.' : 'Du hast die Steuerung. Die Fylgja pausiert nicht automatisch.'" class="xl:col-span-3" flush>
        <div v-if="session?.vnc" ref="screen" class="w-full aspect-video bg-black" />
        <Empty v-else-if="session && !session.available">{{ session.hint }}</Empty>
        <Empty v-else-if="session && st.state !== 'running'">Der Computer schläft. Mit „Starten“ wacht er auf, Dateien und das Browser-Profil sind noch da.</Empty>
        <Empty v-else>Der Computer läuft, hat aber keinen Desktop (Live-Ansicht nicht verfügbar).</Empty>
      </Card>
      <div class="space-y-4">
        <Card title="Was sie gerade tut" subtitle="Live" flush>
          <ul class="text-xs divide-y divide-[var(--line)] max-h-[34vh] overflow-auto" aria-live="polite">
            <li v-for="(f, i) in feed" :key="i" class="px-3 py-2 flex gap-2 items-start"><component :is="icon(f.tool)" class="size-3.5 mt-0.5 muted shrink-0" /><span class="mono break-all">{{ f.text }}</span></li>
          </ul>
          <Empty v-if="!feed.length">Noch nichts. Sobald die Fylgja Shell, Browser oder Desktop nutzt, siehst du es hier.</Empty>
        </Card>
        <Card title="Dateien" subtitle="/home/dot/workspace" flush>
          <ul class="text-xs mono divide-y divide-[var(--line)] max-h-[26vh] overflow-auto">
            <li v-for="f in session?.files || []" :key="f.name" class="px-3 py-1.5 flex gap-2 items-center"><span class="flex-1 truncate">{{ f.dir ? f.name + '/' : f.name }}</span><span class="muted">{{ f.dir ? '' : f.size }}</span>
              <a v-if="!f.dir" :href="`${base}/file?path=${encodeURIComponent(f.name)}`" class="muted hover:text-[var(--text)]" :aria-label="`${f.name} herunterladen`" download><Download class="size-3.5" /></a></li>
          </ul>
          <Empty v-if="!(session?.files || []).length">Leer.</Empty>
        </Card>
      </div>
    </div>
  </div>
</template>
