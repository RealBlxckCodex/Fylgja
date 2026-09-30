<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { get } from '@/lib/api'
import { dt } from '@/lib/format'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Badge from '@/components/Badge.vue'
const toast = useToast()
const entries = ref<any[]>([])
const q = ref('')
const verify = ref<any>(null)
async function load() { entries.value = await get('/audit') }
onMounted(() => load().catch(toast.err))
async function check() { try { verify.value = await get('/audit/verify') } catch (e) { toast.err(e) } }
const shown = computed(() => entries.value.filter((e) => !q.value || JSON.stringify(e).toLowerCase().includes(q.value.toLowerCase())))
function exportJSON() {
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([JSON.stringify(shown.value, null, 2)], { type: 'application/json' }))
  a.download = 'fylgja-audit.json'
  a.click()
}
</script>
<template>
  <div class="space-y-4">
    <div class="flex items-center gap-3 flex-wrap"><h1 class="text-xl font-semibold">Audit</h1>
      <Badge v-if="verify" :tone="verify.ok ? 'ok' : 'err'" :icon="verify.ok ? '✓' : '✕'">{{ verify.ok ? `Hashchain intakt (${verify.entries} Einträge)` : `Kette gebrochen: ${verify.error}` }}</Badge>
      <div class="flex-1" /><input v-model="q" class="input !w-64" placeholder="Filtern…" aria-label="Filtern" /><Btn variant="ghost" @click="exportJSON">Export</Btn><Btn @click="check">Hashchain prüfen</Btn></div>
    <Card flush>
      <table class="dense"><thead><tr><th>#</th><th>Zeit</th><th>Akteur</th><th>Aktion</th><th>Ziel</th><th>Details</th><th>Hash</th></tr></thead>
        <tbody><tr v-for="e in shown" :key="e.id"><td class="mono muted">{{ e.id }}</td><td class="muted whitespace-nowrap">{{ dt(e.at) }}</td><td class="mono text-xs">{{ e.actor }}</td>
          <td><Badge tone="info">{{ e.action }}</Badge></td><td class="mono text-xs">{{ e.target }}</td><td class="mono text-[11px] max-w-md break-all muted">{{ JSON.stringify(e.detail) }}</td><td class="mono text-[11px] muted">{{ e.hash.slice(0, 12) }}</td></tr></tbody></table>
    </Card>
  </div>
</template>
