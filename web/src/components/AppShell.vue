<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { Home, Bot, Network, Server, Monitor, Brain, GraduationCap, ShieldCheck, Euro, ScrollText, Settings, LogOut, Sun, Moon, OctagonPause, Menu } from 'lucide-vue-next'
import { useAuth } from '@/stores/auth'
import { useToast } from '@/stores/toast'
import { get, post } from '@/lib/api'
import { eur, pct, ms } from '@/lib/format'
import { subscribe } from '@/lib/sse'
import Modal from './Modal.vue'
import Btn from './Btn.vue'
const auth = useAuth()
const toast = useToast()
const router = useRouter()
const route = useRoute()
const isActive = (to: string) => (to === '/' ? route.path === '/' : route.path === to || route.path.startsWith(to + '/') || (to === '/rules' && route.path.startsWith('/approvals')))
const nav = [
  { to: '/', label: 'Home', icon: Home },
  { to: '/dots', label: 'Fylgjur', icon: Bot },
  { to: '/teams', label: 'Tasks & Teams', icon: Network },
  { to: '/fleet', label: 'Flotte', icon: Server },
  { to: '/computer', label: 'Computer', icon: Monitor },
  { to: '/memory', label: 'Memory', icon: Brain },
  { to: '/learn', label: 'Lernen', icon: GraduationCap },
  { to: '/rules', label: 'Regeln & Approvals', icon: ShieldCheck },
  { to: '/costs', label: 'Kosten', icon: Euro },
  { to: '/audit', label: 'Audit', icon: ScrollText },
  { to: '/settings', label: 'Einstellungen', icon: Settings },
]
const summary = ref<any>({})
const pending = ref(0)
const brake = ref(false)
const mobileNav = ref(false)
const theme = ref(document.documentElement.dataset.theme === 'light' ? 'light' : 'dark')
function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  if (theme.value === 'light') document.documentElement.dataset.theme = 'light'
  else delete document.documentElement.dataset.theme
  try { localStorage.setItem('fylgja-theme', theme.value) } catch { /* privat */ }
}
async function refresh() {
  try {
    const [fo, ap] = await Promise.all([get('/fleet/overview'), get('/approvals?status=pending')])
    summary.value = fo.summary
    pending.value = ap.length
  } catch { /* offline */ }
}
let timer: number | undefined
let close: (() => void) | undefined
onMounted(() => {
  refresh()
  timer = window.setInterval(refresh, 10000)
  close = subscribe(['approvals', 'alerts'], (t, d) => {
    if (t === 'alerts') toast.push(d.message || d.text || 'Alarm', 'info')
    refresh()
  })
})
onBeforeUnmount(() => { clearInterval(timer); close?.() })
async function pauseAll(hard: boolean) {
  try { const r = await post('/emergency/pause-all', { hard }); toast.ok(`Alle Fylgjur pausiert${hard ? `, ${r.stopped_runs} Läufe gestoppt` : ''}`); brake.value = false }
  catch (e) { toast.err(e) }
}
async function logout() { await auth.logout(); router.push('/login') }
</script>
<template>
  <div class="flex h-full">
    <aside class="w-60 shrink-0 border-r line flex-col bg-[var(--panel)] hidden md:flex" :class="{ '!flex fixed inset-y-0 z-40': mobileNav }">
      <div class="px-4 py-4 flex items-center gap-2">
        <svg viewBox="0 0 64 64" class="size-7" aria-hidden="true"><path d="M32 10c-9 8-14 16-14 24a14 14 0 0 0 28 0c0-8-5-16-14-24z" fill="none" stroke="#B71C2A" stroke-width="4"/><circle cx="32" cy="36" r="5" fill="#B71C2A"/></svg>
        <div><div class="font-semibold tracking-wide">Fylgja</div><div class="text-[11px] muted truncate max-w-36">{{ auth.workspace }}</div></div>
      </div>
      <nav class="flex-1 px-2 space-y-0.5 overflow-y-auto" @click="mobileNav = false">
        <RouterLink v-for="n in nav" :key="n.to" :to="n.to" class="focus-ring flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm muted hover:text-[var(--text)] hover:bg-[var(--panel-2)]"
          :class="isActive(n.to) ? 'text-[var(--text)]! bg-[var(--panel-2)]' : ''" :aria-current="isActive(n.to) ? 'page' : undefined">
          <component :is="n.icon" class="size-4" aria-hidden="true" />
          <span class="flex-1">{{ n.label }}</span>
          <span v-if="n.to === '/rules' && pending" class="mono text-[11px] rounded bg-accent text-white px-1.5">{{ pending }}</span>
        </RouterLink>
      </nav>
      <div class="p-3 border-t line text-xs flex items-center gap-2">
        <div class="flex-1 min-w-0"><div class="truncate">{{ auth.principal?.display_name }}</div><div class="muted">{{ auth.principal?.role }}</div></div>
        <button class="focus-ring p-1.5 rounded hover:bg-[var(--panel-2)]" :aria-label="theme === 'dark' ? 'Hell' : 'Dunkel'" @click="toggleTheme"><Sun v-if="theme === 'dark'" class="size-4" /><Moon v-else class="size-4" /></button>
        <button class="focus-ring p-1.5 rounded hover:bg-[var(--panel-2)]" aria-label="Abmelden" @click="logout"><LogOut class="size-4" /></button>
      </div>
    </aside>
    <div class="flex-1 min-w-0 flex flex-col">
      <header class="h-12 border-b line flex items-center gap-4 px-4 text-xs shrink-0">
        <button class="md:hidden focus-ring" aria-label="Menü" @click="mobileNav = !mobileNav"><Menu class="size-5" /></button>
        <RouterLink to="/fleet" class="flex items-center gap-4 muted hover:text-[var(--text)] overflow-x-auto whitespace-nowrap">
          <span>GPU <b class="mono text-[var(--text)]">{{ pct(summary.gpu_util) }}</b></span>
          <span class="hidden sm:inline">Nodes <b class="mono text-[var(--text)]">{{ summary.nodes_ready ?? 0 }}/{{ summary.nodes_total ?? 0 }}</b></span>
          <span class="hidden sm:inline">Queue p95 <b class="mono text-[var(--text)]">{{ ms(summary.queue_wait_p95_ms) }}</b></span>
          <span>Heute <b class="mono text-[var(--text)]">{{ eur(summary.cost_today_micro_eur) }}</b></span>
        </RouterLink>
        <div class="flex-1" />
        <Btn v-if="auth.can('own')" size="sm" variant="danger" @click="brake = true"><OctagonPause class="size-3.5" /> Notbremse</Btn>
      </header>
      <main class="flex-1 overflow-auto p-4 md:p-6"><slot /></main>
    </div>
    <Modal :open="brake" title="Notbremse" @close="brake = false">
      <p class="text-sm muted mb-4">„Alles pausieren“ stoppt neue Läufe, laufende enden sauber. „Hart stoppen“ bricht zusätzlich alle laufenden und wartenden Läufe ab.</p>
      <div class="flex gap-2 justify-end">
        <Btn variant="ghost" @click="pauseAll(false)">Alles pausieren</Btn>
        <Btn variant="danger" @click="pauseAll(true)">Hart stoppen</Btn>
      </div>
    </Modal>
  </div>
</template>
