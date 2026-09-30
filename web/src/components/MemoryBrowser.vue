<script setup lang="ts">
import { ref, watch } from 'vue'
import { get, post, patch, del } from '@/lib/api'
import { dt, short } from '@/lib/format'
import { useToast } from '@/stores/toast'
import Btn from './Btn.vue'
import Badge from './Badge.vue'
import Empty from './Empty.vue'
const props = defineProps<{ dot: string }>()
const toast = useToast()
const items = ref<any[]>([])
const q = ref('')
const tier = ref('')
const add = ref({ tier: 'semantic', content: '', sensitivity: 'normal' })
const editing = ref<string | null>(null)
const editText = ref('')
async function load() {
  const qs = q.value ? `?q=${encodeURIComponent(q.value)}` : tier.value ? `?tier=${tier.value}` : ''
  items.value = await get(`/dots/${props.dot}/memory${qs}`)
}
watch(() => [props.dot, tier.value], () => load().catch(toast.err), { immediate: true })
async function create() {
  try { await post(`/dots/${props.dot}/memory`, add.value); add.value.content = ''; toast.ok('Gespeichert'); load() } catch (e) { toast.err(e) }
}
async function save(m: any) {
  try { await patch(`/memory/${m.id}`, { content: editText.value }); editing.value = null; load() } catch (e) { toast.err(e) }
}
async function pin(m: any) { try { await patch(`/memory/${m.id}`, { pinned: !m.pinned }); load() } catch (e) { toast.err(e) } }
async function remove(m: any) {
  if (!confirm('Endgültig löschen (inkl. abgeleiteter Einträge)?')) return
  try { const r = await del(`/memory/${m.id}`); toast.ok(`${r.deleted} Einträge gelöscht`); load() } catch (e) { toast.err(e) }
}
async function exportMd() {
  const files = await get(`/dots/${props.dot}/memory/export`)
  const text = Object.entries(files).map(([p, c]) => `<!-- ${p} -->\n${c}`).join('\n\n')
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([text], { type: 'text/markdown' }))
  a.download = 'fylgja-memory.md'
  a.click()
}
const tone: Record<string, any> = { core: 'accent', semantic: 'info', episodic: 'muted', procedural: 'ok', note: 'warn' }
</script>
<template>
  <div class="space-y-4">
    <div class="flex flex-wrap gap-2 items-center">
      <form class="flex gap-2 flex-1 min-w-60" @submit.prevent="load"><input v-model="q" class="input" placeholder="Hybride Suche (Vektor + Volltext)…" aria-label="Memory durchsuchen" /><Btn type="submit" variant="ghost">Suchen</Btn></form>
      <select v-model="tier" class="input !w-40" aria-label="Stufe"><option value="">Alle Stufen</option><option v-for="t in ['core', 'semantic', 'episodic', 'procedural', 'note']" :key="t" :value="t">{{ t }}</option></select>
      <Btn variant="ghost" @click="exportMd">Markdown-Export</Btn>
    </div>
    <form class="panel panel-2 p-3 flex flex-wrap gap-2 items-center" @submit.prevent="create">
      <select v-model="add.tier" class="input !w-36" aria-label="Stufe"><option v-for="t in ['semantic', 'core', 'procedural', 'note']" :key="t" :value="t">{{ t }}</option></select>
      <input v-model="add.content" class="input flex-1 min-w-60" placeholder="Neuer Eintrag (z. B. „Anna ist meine Chefin“)" required />
      <select v-model="add.sensitivity" class="input !w-40" aria-label="Sensibilität"><option value="normal">normal</option><option value="private">privat</option><option value="secret-adjacent">sensibel</option></select>
      <Btn type="submit">Merken</Btn>
    </form>
    <ul class="space-y-2">
      <li v-for="m in items" :key="m.id" class="panel p-3">
        <div class="flex items-start gap-3">
          <div class="flex flex-col gap-1 items-start shrink-0 w-24">
            <Badge :tone="tone[m.tier]">{{ m.tier }}</Badge>
            <Badge v-if="m.origin === 'untrusted'" tone="err" icon="⚠">untrusted</Badge>
            <Badge v-if="m.pinned" tone="accent" icon="📌">gepinnt</Badge>
          </div>
          <div class="flex-1 min-w-0">
            <textarea v-if="editing === m.id" v-model="editText" class="input" rows="2" />
            <p v-else class="text-sm whitespace-pre-wrap">{{ m.content }}</p>
            <p class="text-[11px] muted mt-1 mono">{{ short(m.id) }} · {{ dt(m.created_at) }} · Wichtigkeit {{ m.importance.toFixed(2) }} · {{ m.sensitivity }}<span v-if="m.why"> · {{ m.why }}</span><span v-if="m.superseded_by"> · ersetzt</span></p>
          </div>
          <div class="flex gap-1 shrink-0">
            <template v-if="editing === m.id"><Btn size="sm" @click="save(m)">Speichern</Btn><Btn size="sm" variant="subtle" @click="editing = null">Abbrechen</Btn></template>
            <template v-else>
              <Btn size="sm" variant="subtle" @click="editing = m.id; editText = m.content">Bearbeiten</Btn>
              <Btn size="sm" variant="subtle" @click="pin(m)">{{ m.pinned ? 'Lösen' : 'Pinnen' }}</Btn>
              <Btn size="sm" variant="subtle" @click="remove(m)">Löschen</Btn>
            </template>
          </div>
        </div>
      </li>
    </ul>
    <Empty v-if="!items.length">Keine Einträge.</Empty>
  </div>
</template>
