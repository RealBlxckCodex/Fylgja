<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRouter } from 'vue-router'
import { get } from '@/lib/api'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Empty from '@/components/Empty.vue'
const props = defineProps<{ id?: string }>()
const router = useRouter()
const toast = useToast()
const dots = ref<any[]>([])
const session = ref<any>(null)
const screen = ref<HTMLDivElement>()
const viewOnly = ref(true)
let rfb: any = null
async function connect() {
  if (!props.id) return
  session.value = await get(`/dots/${props.id}/computer`).catch((e) => { toast.err(e); return null })
  if (!session.value?.vnc || !screen.value) return
  const { default: RFB } = await import('@novnc/novnc')
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  rfb?.disconnect()
  rfb = new RFB(screen.value, `${proto}://${location.host}${session.value.vnc_path}`)
  rfb.viewOnly = viewOnly.value
  rfb.scaleViewport = true
}
function takeover() {
  viewOnly.value = !viewOnly.value
  if (rfb) rfb.viewOnly = viewOnly.value
  toast.push(viewOnly.value ? 'Steuerung an die Fylgja zurückgegeben' : 'Du steuerst jetzt (Take-over wird auditiert)', 'info')
}
onMounted(async () => { dots.value = await get('/dots'); if (!props.id && dots.value[0]) router.replace(`/computer/${dots.value[0].dot.id}`); else connect() })
watch(() => props.id, connect)
onBeforeUnmount(() => rfb?.disconnect())
</script>
<template>
  <div class="space-y-4">
    <div class="flex items-center gap-3 flex-wrap">
      <h1 class="text-xl font-semibold">Computer</h1>
      <select class="input !w-56" :value="id" aria-label="Fylgja" @change="router.push(`/computer/${($event.target as HTMLSelectElement).value}`)"><option v-for="d in dots" :key="d.dot.id" :value="d.dot.id">{{ d.dot.name }}</option></select>
      <div class="flex-1" />
      <Btn v-if="session?.vnc" :variant="viewOnly ? 'primary' : 'danger'" @click="takeover">{{ viewOnly ? '🖐 Übernehmen' : '↩ Zurückgeben' }}</Btn>
    </div>
    <div class="grid gap-4 xl:grid-cols-4">
      <Card title="Live-Bildschirm" subtitle="noVNC über den Control Plane (Token an Sitzung gebunden)" class="xl:col-span-3" flush>
        <div v-if="session?.vnc" ref="screen" class="w-full aspect-video bg-black" />
        <Empty v-else-if="session && !session.available">{{ session.hint }}</Empty>
        <Empty v-else>Live-View nicht verfügbar (Sandbox ohne Desktop oder noch nicht gestartet).</Empty>
      </Card>
      <Card title="Dateien" subtitle="/home/dot/workspace (read-only)" flush>
        <ul class="text-xs mono divide-y divide-[var(--line)] max-h-[60vh] overflow-auto">
          <li v-for="f in session?.files || []" :key="f.name" class="px-3 py-1.5 flex gap-2"><span>{{ f.dir ? '📁' : '📄' }}</span><span class="flex-1 truncate">{{ f.name }}</span><span class="muted">{{ f.dir ? '' : f.size }}</span></li>
        </ul>
        <Empty v-if="!(session?.files || []).length">Leer.</Empty>
      </Card>
    </div>
  </div>
</template>
