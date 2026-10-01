<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Home, Bot, Network, Server, Monitor, Brain, GraduationCap, ShieldCheck, Euro, ScrollText, Settings, LogOut, Sun, Moon, OctagonPause, Menu, Search } from 'lucide-vue-next'
import { useAuth } from '@/stores/auth'
import { useToast } from '@/stores/toast'
import { get, post } from '@/lib/api'
import { eur, pct, ms } from '@/lib/format'
import { subscribe } from '@/lib/sse'
import Modal from './Modal.vue'
import Btn from './Btn.vue'
import Emblem from './Emblem.vue'
import CommandPalette from './CommandPalette.vue'
const auth = useAuth()
const toast = useToast()
const router = useRouter()
const route = useRoute()
const groups = [
  { title: 'Arbeit', items: [{ to: '/', label: 'Home', icon: Home }, { to: '/dots', label: 'Fylgjur', icon: Bot }, { to: '/teams', label: 'Tasks & Teams', icon: Network }, { to: '/computer', label: 'Computer', icon: Monitor }] },
  { title: 'Wissen', items: [{ to: '/memory', label: 'Memory', icon: Brain }, { to: '/learn', label: 'Lernen', icon: GraduationCap }] },
  { title: 'Betrieb', items: [{ to: '/fleet', label: 'Flotte', icon: Server }, { to: '/rules', label: 'Regeln & Freigaben', icon: ShieldCheck }, { to: '/costs', label: 'Kosten', icon: Euro }, { to: '/audit', label: 'Audit', icon: ScrollText }, { to: '/settings', label: 'Einstellungen', icon: Settings }] },
]
const isActive = (to: string) => (to === '/' ? route.path === '/' : route.path === to || route.path.startsWith(to + '/') || (to === '/rules' && route.path.startsWith('/approvals')))
const summary = ref<any>({})
const pending = ref(0)
const brake = ref(false)
const mobileNav = ref(false)
const palette = ref<InstanceType<typeof CommandPalette>>()
const theme = ref(document.documentElement.dataset.theme === 'light' ? 'light' : 'dark')
const title = computed(() => groups.flatMap((g) => g.items).find((i) => isActive(i.to) && i.to !== '/')?.label ?? (route.path === '/' ? 'Home' : ''))
function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  if (theme.value === 'light') document.documentElement.dataset.theme = 'light'; else delete document.documentElement.dataset.theme
  try { localStorage.setItem('fylgja-theme', theme.value) } catch { /* privat */ }
}
async function refresh() {
  try {
    const [fo, ap] = await Promise.all([get('/fleet/overview'), get('/approvals?status=pending')])
    summary.value = fo.summary; pending.value = ap.length
  } catch { /* offline */ }
}
let timer: number | undefined
let close: (() => void) | undefined
onMounted(() => {
  refresh(); timer = window.setInterval(refresh, 10000)
  close = subscribe(['approvals', 'alerts'], (t, d) => { if (t === 'alerts') toast.push(d.message || d.text || 'Alarm', 'info'); refresh() })
})
onBeforeUnmount(() => { clearInterval(timer); close?.() })
async function pauseAll(hard: boolean) {
  try { const r = await post('/emergency/pause-all', { hard }); toast.ok(`Alle Fylgjur pausiert${hard ? `, ${r.stopped_runs} Läufe gestoppt` : ''}`); brake.value = false } catch (e) { toast.err(e) }
}
async function logout() { await auth.logout(); router.push('/login') }
const initials = computed(() => (auth.principal?.display_name || '?').split(' ').map((p) => p[0]).slice(0, 2).join('').toUpperCase())
</script>
<template>
  <div class="flex h-full">
    <div v-if="mobileNav" class="fixed inset-0 bg-black/60 z-30 md:hidden" @click="mobileNav = false" />
    <aside class="w-64 shrink-0 flex-col hidden md:flex m-3 mr-0 rounded-2xl panel" :class="{ '!flex fixed inset-y-0 left-0 z-40 !m-0 !rounded-none': mobileNav }">
      <div class="px-5 pt-5 pb-4 flex items-center gap-3">
        <Emblem :size="34" glow />
        <div class="min-w-0"><div class="font-semibold text-[17px] tracking-[0.12em] text-gradient">FYLGJA</div><div class="text-[11px] muted truncate">{{ auth.workspace }}</div></div>
      </div>
      <button class="mx-4 mb-3 flex items-center gap-2 px-3 py-2 rounded-xl text-sm muted hover:text-[var(--text)] focus-ring" style="background: var(--panel-2); box-shadow: inset 0 0 0 1px var(--line)" @click="palette?.toggle()">
        <Search class="size-3.5" /><span class="flex-1 text-left">Suchen …</span><span class="kbd">Strg K</span>
      </button>
      <nav class="flex-1 px-3 overflow-y-auto space-y-5 pb-3" @click="mobileNav = false">
        <div v-for="g in groups" :key="g.title">
          <div class="px-3 pb-1.5 text-[10px] uppercase tracking-[0.14em] muted/70">{{ g.title }}</div>
          <RouterLink v-for="n in g.items" :key="n.to" :to="n.to" :aria-current="isActive(n.to) ? 'page' : undefined"
            class="focus-ring relative flex items-center gap-3 px-3 py-2 rounded-xl text-[13.5px] transition-colors"
            :class="isActive(n.to) ? 'text-[var(--text)] font-medium' : 'muted hover:text-[var(--text)] hover:bg-[var(--panel-2)]'"
            :style="isActive(n.to) ? 'background: linear-gradient(90deg, var(--accent-glow), transparent 85%)' : ''">
            <span v-if="isActive(n.to)" class="absolute left-0 top-2 bottom-2 w-[3px] rounded-full" style="background: var(--accent-2); box-shadow: 0 0 10px var(--accent-2)" />
            <component :is="n.icon" class="size-[17px]" aria-hidden="true" />
            <span class="flex-1">{{ n.label }}</span>
            <span v-if="n.to === '/rules' && pending" class="mono text-[10.5px] rounded-full bg-accent text-white px-1.5 py-px aura">{{ pending }}</span>
          </RouterLink>
        </div>
      </nav>
      <div class="p-3 m-3 mt-0 rounded-xl flex items-center gap-2.5" style="background: var(--panel-2)">
        <div class="size-8 rounded-full flex items-center justify-center text-xs font-semibold text-white bg-accent shrink-0">{{ initials }}</div>
        <div class="flex-1 min-w-0 leading-tight"><div class="text-[13px] truncate">{{ auth.principal?.display_name }}</div><div class="text-[11px] muted">{{ auth.principal?.role }}</div></div>
        <button class="focus-ring p-1.5 rounded-lg hover:bg-[var(--panel-2)] muted hover:text-[var(--text)]" :aria-label="theme === 'dark' ? 'Helles Design' : 'Dunkles Design'" @click="toggleTheme"><Sun v-if="theme === 'dark'" class="size-4" /><Moon v-else class="size-4" /></button>
        <button class="focus-ring p-1.5 rounded-lg hover:bg-[var(--panel-2)] muted hover:text-[var(--text)]" aria-label="Abmelden" @click="logout"><LogOut class="size-4" /></button>
      </div>
    </aside>
    <div class="flex-1 min-w-0 flex flex-col">
      <header class="h-16 flex items-center gap-3 px-4 md:px-6 shrink-0">
        <button class="md:hidden focus-ring p-1" aria-label="Menü" @click="mobileNav = !mobileNav"><Menu class="size-5" /></button>
        <h1 class="hidden sm:block text-[15px] font-medium muted">{{ title }}</h1>
        <div class="flex-1" />
        <RouterLink to="/fleet" class="hidden lg:flex items-center gap-1 rounded-full px-1.5 py-1 text-xs panel" aria-label="Flottenstatus">
          <span class="flex items-center gap-1.5 px-2.5 py-1"><span class="size-1.5 rounded-full" :style="{ background: (summary.nodes_ready ?? 0) > 0 ? 'var(--ok)' : 'var(--muted)', boxShadow: (summary.nodes_ready ?? 0) > 0 ? '0 0 8px var(--ok)' : 'none' }" /><span class="muted">Nodes</span><b class="mono">{{ summary.nodes_ready ?? 0 }}/{{ summary.nodes_total ?? 0 }}</b></span>
          <span class="w-px h-4" style="background: var(--line)" /><span class="px-2.5 py-1"><span class="muted">GPU</span> <b class="mono">{{ pct(summary.gpu_util) }}</b></span>
          <span class="w-px h-4" style="background: var(--line)" /><span class="px-2.5 py-1"><span class="muted">Queue</span> <b class="mono">{{ ms(summary.queue_wait_p95_ms) }}</b></span>
          <span class="w-px h-4" style="background: var(--line)" /><span class="px-2.5 py-1"><span class="muted">Heute</span> <b class="mono">{{ eur(summary.cost_today_micro_eur) }}</b></span>
        </RouterLink>
        <RouterLink v-if="pending" to="/rules" class="focus-ring flex items-center gap-2 rounded-full px-3 py-1.5 text-xs font-medium aura" style="background: color-mix(in srgb, var(--warn) 14%, transparent); color: var(--warn); box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--warn) 35%, transparent)">🛡 {{ pending }} Freigabe{{ pending > 1 ? 'n' : '' }}</RouterLink>
        <Btn v-if="auth.can('own')" size="sm" variant="danger" @click="brake = true"><OctagonPause class="size-3.5" /> Notbremse</Btn>
      </header>
      <main class="flex-1 overflow-auto px-4 md:px-6 pb-8"><div class="mx-auto max-w-[1400px]"><slot /></div></main>
    </div>
    <CommandPalette ref="palette" />
    <Modal :open="brake" title="Notbremse" @close="brake = false">
      <p class="text-sm muted mb-5">„Alles pausieren“ stoppt neue Läufe, laufende enden sauber. „Hart stoppen“ bricht zusätzlich alle laufenden und wartenden Läufe ab.</p>
      <div class="flex gap-2 justify-end"><Btn variant="ghost" @click="pauseAll(false)">Alles pausieren</Btn><Btn variant="danger" @click="pauseAll(true)">Hart stoppen</Btn></div>
    </Modal>
  </div>
</template>
