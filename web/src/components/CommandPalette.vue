<script setup lang="ts">
// Strg/⌘+K: schnelle Navigation und Aktionen.
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { Search, CornerDownLeft } from 'lucide-vue-next'
import { get } from '@/lib/api'
const open = ref(false)
const q = ref('')
const idx = ref(0)
const input = ref<HTMLInputElement>()
const router = useRouter()
const dots = ref<any[]>([])
const base = [
  { label: 'Home', to: '/', hint: 'Command Center' }, { label: 'Fylgjur', to: '/dots' }, { label: 'Tasks & Teams', to: '/teams' },
  { label: 'Flotte', to: '/fleet', hint: 'GPU, Queue, Agenten' }, { label: 'Computer', to: '/computer' }, { label: 'Memory', to: '/memory' },
  { label: 'Lernen', to: '/learn' }, { label: 'Regeln & Approvals', to: '/rules' }, { label: 'Kosten', to: '/costs' },
  { label: 'Audit', to: '/audit' }, { label: 'Einstellungen', to: '/settings' },
]
const items = computed(() => {
  const all = [...base, ...dots.value.flatMap((d) => [{ label: `Chat mit ${d.dot.name}`, to: `/dots/${d.dot.id}`, hint: 'Fylgja' }, { label: `${d.dot.name}: Memory`, to: `/memory/${d.dot.id}` }, { label: `${d.dot.name}: Computer`, to: `/computer/${d.dot.id}` }])]
  const t = q.value.toLowerCase().trim()
  return t ? all.filter((i) => i.label.toLowerCase().includes(t) || (i.hint || '').toLowerCase().includes(t)) : all
})
watch(q, () => (idx.value = 0))
function go(i: any) { if (!i) return; open.value = false; router.push(i.to) }
function key(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); open.value = !open.value; if (open.value) { q.value = ''; get('/dots').then((d) => (dots.value = d)).catch(() => {}); nextTick(() => input.value?.focus()) } }
  else if (e.key === 'Escape') open.value = false
}
onMounted(() => window.addEventListener('keydown', key)); onBeforeUnmount(() => window.removeEventListener('keydown', key))
defineExpose({ toggle: () => key(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true })) })
</script>
<template>
  <Teleport to="body">
    <div v-if="open" class="fixed inset-0 z-[60] flex items-start justify-center pt-[14vh] px-4 bg-black/60 backdrop-blur-sm" @click.self="open = false">
      <div class="panel w-full max-w-xl overflow-hidden rise" role="dialog" aria-label="Befehlspalette">
        <div class="flex items-center gap-3 px-4 border-b line"><Search class="size-4 muted" /><input ref="input" v-model="q" class="flex-1 bg-transparent py-3.5 outline-none" placeholder="Suchen oder springen …" aria-label="Suchen"
          @keydown.down.prevent="idx = Math.min(idx + 1, items.length - 1)" @keydown.up.prevent="idx = Math.max(idx - 1, 0)" @keydown.enter.prevent="go(items[idx])" /><span class="kbd">esc</span></div>
        <ul class="max-h-80 overflow-auto p-2">
          <li v-for="(i, n) in items" :key="i.to + i.label"><button class="w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm text-left" :class="n === idx ? 'bg-[var(--panel-2)] text-[var(--text)]' : 'muted'" @mouseenter="idx = n" @click="go(i)">
            <span class="flex-1">{{ i.label }}</span><span v-if="i.hint" class="text-xs muted">{{ i.hint }}</span><CornerDownLeft v-if="n === idx" class="size-3.5" /></button></li>
          <li v-if="!items.length" class="px-3 py-6 text-center text-sm muted">Nichts gefunden.</li>
        </ul>
      </div>
    </div>
  </Teleport>
</template>
