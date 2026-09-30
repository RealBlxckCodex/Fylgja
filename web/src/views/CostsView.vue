<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { get, put } from '@/lib/api'
import { eur, num } from '@/lib/format'
import { useToast } from '@/stores/toast'
import { useAuth } from '@/stores/auth'
import Card from '@/components/Card.vue'
import Chart from '@/components/Chart.vue'
import Btn from '@/components/Btn.vue'
const toast = useToast()
const auth = useAuth()
const data = ref<any>(null)
const ws = ref({ limit: 50, hard: false })
async function load() { data.value = await get('/usage') }
onMounted(() => load().catch(toast.err))
const daily = computed(() => ({ xAxis: { type: 'category', data: (data.value?.daily || []).map((d: any) => d.day.slice(5)) }, yAxis: { type: 'value', name: '€' },
  series: [{ type: 'bar', data: (data.value?.daily || []).map((d: any) => (d.cost_micro_eur / 1e6).toFixed(3)) }] }))
const total = computed(() => (data.value?.by_dot || []).reduce((s: number, x: any) => s + x.cost_micro_eur, 0))
function csv() {
  const rows = [['gruppe', 'schlüssel', 'kosten_eur', 'tokens_in', 'tokens_out', 'tokens_cached', 'aufrufe']]
  for (const g of ['by_dot', 'by_model', 'by_deployment']) for (const r of data.value[g] || []) rows.push([g, r.key, (r.cost_micro_eur / 1e6).toFixed(4), r.tokens_in, r.tokens_out, r.tokens_cached, r.calls])
  const a = document.createElement('a')
  a.href = URL.createObjectURL(new Blob([rows.map((r) => r.join(';')).join('\n')], { type: 'text/csv' }))
  a.download = 'fylgja-kosten.csv'
  a.click()
}
async function saveWs() {
  try { await put('/budgets', { scope: 'workspace', period: 'month', limit_micro_eur: Math.round(ws.value.limit * 1e6), hard: ws.value.hard }); toast.ok('Budget gespeichert'); load() } catch (e) { toast.err(e) }
}
</script>
<template>
  <div v-if="data" class="space-y-4">
    <div class="flex items-center gap-3"><h1 class="text-xl font-semibold">Kosten</h1><span class="text-sm muted">Monat bisher <b class="mono">{{ eur(total) }}</b></span><div class="flex-1" /><Btn variant="ghost" size="sm" @click="csv">CSV-Export</Btn></div>
    <Card title="Tageskosten (Monat)"><Chart :option="daily" height="220px" /></Card>
    <div class="grid gap-4 xl:grid-cols-3">
      <Card v-for="g in [['by_dot', 'Je Fylgja'], ['by_model', 'Je logischem Modell'], ['by_deployment', 'Je Deployment/Node']]" :key="g[0]" :title="g[1]" flush>
        <table class="dense"><thead><tr><th>{{ g[1].split(' ').pop() }}</th><th class="text-right">Kosten</th><th class="text-right">Tokens</th><th class="text-right">Cache</th></tr></thead>
          <tbody><tr v-for="r in data[g[0]] || []" :key="r.key"><td class="truncate max-w-40">{{ r.key }}</td><td class="text-right mono">{{ eur(r.cost_micro_eur, 3) }}</td>
            <td class="text-right mono">{{ num(r.tokens_in + r.tokens_out) }}</td><td class="text-right mono">{{ r.tokens_in ? Math.round((100 * r.tokens_cached) / r.tokens_in) : 0 }} %</td></tr></tbody></table>
      </Card>
    </div>
    <Card title="Budgets" subtitle="Soft (80 %) → günstigeres Modell + Hinweis · Hard (100 %) → Stopp + Proposal">
      <table class="dense mb-4"><thead><tr><th>Geltung</th><th>Zeitraum</th><th>Limit</th><th>Art</th></tr></thead>
        <tbody><tr v-for="b in data.budgets" :key="b.id"><td>{{ b.scope }} {{ b.name }}</td><td>{{ b.period }}</td><td class="mono">{{ eur(b.limit_micro_eur) }}</td><td>{{ b.hard ? 'hart' : 'weich' }}</td></tr></tbody></table>
      <form v-if="auth.can('own')" class="flex flex-wrap gap-2 items-center text-sm" @submit.prevent="saveWs">
        Workspace-Monatsbudget <input v-model.number="ws.limit" type="number" min="1" class="input !w-28" /> € <label class="flex gap-1 items-center"><input v-model="ws.hard" type="checkbox" /> hart</label><Btn type="submit" size="sm">Setzen</Btn>
      </form>
    </Card>
  </div>
</template>
