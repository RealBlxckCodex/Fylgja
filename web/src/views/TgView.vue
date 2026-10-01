<script setup lang="ts">
// Telegram Mini App: Freigaben und Chat in Telegram. Anmeldung über die signierten initData.
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { get, post } from '@/lib/api'
import { subscribe } from '@/lib/sse'
import ChatPanel from '@/components/ChatPanel.vue'
import Emblem from '@/components/Emblem.vue'

const state = ref<'loading' | 'ready' | 'denied' | 'outside'>('loading')
const tab = ref<'approvals' | 'chat'>('approvals')
const approvals = ref<any[]>([])
const dots = ref<any[]>([])
const dotId = ref('')
const err = ref('')
let close: (() => void) | undefined

function loadTelegramScript(): Promise<any> {
  return new Promise((resolve) => {
    if ((window as any).Telegram?.WebApp) return resolve((window as any).Telegram.WebApp)
    const s = document.createElement('script')
    s.src = 'https://telegram.org/js/telegram-web-app.js'
    s.onload = () => resolve((window as any).Telegram?.WebApp)
    s.onerror = () => resolve(null)
    document.head.appendChild(s)
  })
}
async function refresh() {
  approvals.value = await get('/approvals?status=pending')
}
async function resolve(a: any, approve: boolean) {
  try { await post(`/approvals/${a.id}/resolve`, { approve }); await refresh() } catch (e: any) { err.value = e?.message || 'Fehlgeschlagen' }
}
onMounted(async () => {
  const wa = await loadTelegramScript()
  if (!wa?.initData) { state.value = 'outside'; return }
  wa.ready(); wa.expand()
  try {
    await post('/auth/telegram/webapp', { init_data: wa.initData })
  } catch { state.value = 'denied'; return }
  dots.value = await get('/dots')
  dotId.value = dots.value[0]?.dot.id ?? ''
  await refresh()
  state.value = 'ready'
  close = subscribe(['approvals'], () => refresh().catch(() => {}))
})
onBeforeUnmount(() => close?.())
</script>

<template>
  <div class="h-dvh flex flex-col overflow-hidden" style="background: var(--bg)">
    <div v-if="state === 'loading'" class="m-auto muted text-sm">Lädt …</div>
    <div v-else-if="state === 'outside'" class="m-auto p-8 text-center max-w-sm space-y-3"><Emblem :size="36" class="mx-auto" /><p class="text-sm">Diese Seite ist für Telegram gedacht. Öffne sie über das Menü deines Fylgja-Bots.</p></div>
    <div v-else-if="state === 'denied'" class="m-auto p-8 text-center max-w-sm space-y-3"><Emblem :size="36" class="mx-auto" /><p class="text-sm">Kein Zugriff. Koppele zuerst dein Telegram-Konto mit <span class="mono">/pair CODE</span> im Chat mit dem Bot.</p></div>
    <template v-else>
      <nav class="flex gap-1 p-2 border-b border-[var(--line)] sticky top-0 z-10" style="background: var(--bg)">
        <button class="flex-1 py-2 rounded-lg text-sm" :class="tab === 'approvals' ? 'bg-[var(--panel-2)] font-medium' : 'muted'" @click="tab = 'approvals'">Freigaben <span v-if="approvals.length" class="ml-1 text-xs rounded-full px-1.5 py-0.5 bg-accent text-white">{{ approvals.length }}</span></button>
        <button class="flex-1 py-2 rounded-lg text-sm" :class="tab === 'chat' ? 'bg-[var(--panel-2)] font-medium' : 'muted'" @click="tab = 'chat'">Chat</button>
      </nav>
      <main v-if="tab === 'approvals'" class="p-3 space-y-3 overflow-auto">
        <p v-if="err" class="text-sm" style="color: var(--err)">{{ err }}</p>
        <p v-if="!approvals.length" class="text-sm muted text-center py-10">Nichts offen.</p>
        <div v-for="a in approvals" :key="a.id" class="rounded-2xl p-4 panel-glow" style="background: var(--panel-2)">
          <div class="text-sm font-medium">{{ a.preview?.summary || a.tool }}</div>
          <div class="text-xs muted mt-1">{{ a.class }} · Risiko {{ a.risk }}</div>
          <p v-if="a.reason" class="text-xs muted mt-1">{{ a.reason }}</p>
          <p v-if="a.step_up" class="text-xs mt-2" style="color: var(--warn)">Diese Freigabe braucht eine Bestätigung per Passkey. Bitte im Web-UI öffnen.</p>
          <div v-else class="flex gap-2 mt-3">
            <button class="flex-1 bg-accent text-white text-sm font-medium py-2 rounded-xl" @click="resolve(a, true)">Freigeben</button>
            <button class="flex-1 text-sm py-2 rounded-xl border border-[var(--line-strong)]" @click="resolve(a, false)">Ablehnen</button>
          </div>
        </div>
      </main>
      <div v-else class="flex-1 flex flex-col min-h-0">
        <select v-if="dots.length > 1" v-model="dotId" class="input mx-2 mt-2 !w-[calc(100%-1rem)]" aria-label="Fylgja"><option v-for="d in dots" :key="d.dot.id" :value="d.dot.id">{{ d.dot.name }}</option></select>
        <div v-if="dotId" class="flex-1 min-h-0"><ChatPanel :key="dotId" :dot="{ id: dotId, name: dots.find((d) => d.dot.id === dotId)?.dot.name }" /></div>
      </div>
    </template>
  </div>
</template>
