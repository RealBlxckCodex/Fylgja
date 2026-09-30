<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { get, post } from '@/lib/api'
import { eur } from '@/lib/format'
import { useAuth } from '@/stores/auth'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Status from '@/components/Status.vue'
import Btn from '@/components/Btn.vue'
import Modal from '@/components/Modal.vue'
const auth = useAuth()
const toast = useToast()
const router = useRouter()
const dots = ref<any[]>([])
const open = ref(false)
const f = ref({ name: '', kind: 'personal', persona: '', charter: '', privacy_mode: 'self_hosted_only' })
async function load() { dots.value = await get('/dots') }
onMounted(() => load().catch(toast.err))
async function create() {
  try { const d = await post('/dots', f.value); open.value = false; router.push(`/dots/${d.id}`) } catch (e) { toast.err(e) }
}
</script>
<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between"><h1 class="text-xl font-semibold">Fylgjur</h1><Btn v-if="auth.can('manage')" @click="open = true">+ Neue Fylgja</Btn></div>
    <Card flush>
      <table class="dense">
        <thead><tr><th>Name</th><th>Art</th><th>Status</th><th>Autonomie</th><th>Privacy</th><th>Läufe</th><th>Freigaben</th><th>Computer</th><th class="text-right">Kosten heute</th></tr></thead>
        <tbody>
          <tr v-for="d in dots" :key="d.dot.id" class="cursor-pointer" @click="router.push(`/dots/${d.dot.id}`)">
            <td class="font-medium">{{ d.dot.name }}</td><td class="muted">{{ d.dot.kind }}</td><td><Status :s="d.dot.status" /></td>
            <td class="mono">L{{ d.dot.autonomy_level }}</td><td class="muted">{{ d.dot.privacy_mode }}</td>
            <td class="mono">{{ d.running }}/{{ d.waiting }}</td><td class="mono">{{ d.pending_approvals }}</td><td><Status :s="d.sandbox || 'stopped'" /></td>
            <td class="text-right mono">{{ eur(d.cost_today_micro_eur) }}</td>
          </tr>
        </tbody>
      </table>
    </Card>
    <Modal :open="open" title="Neue Fylgja" @close="open = false">
      <form class="space-y-3" @submit.prevent="create">
        <div><label class="text-xs muted">Name</label><input v-model="f.name" class="input mt-1" required /></div>
        <div><label class="text-xs muted">Art</label>
          <select v-model="f.kind" class="input mt-1"><option value="personal">Persönlich</option><option value="specialist">Specialist (startet im Shadow-Mode, L0)</option></select></div>
        <div><label class="text-xs muted">Persona</label><input v-model="f.persona" class="input mt-1" placeholder="z. B. ruhig, präzise, knapp" /></div>
        <div><label class="text-xs muted">Charter (Verantwortung, Nicht-Ziele)</label><textarea v-model="f.charter" class="input mt-1" rows="4" /></div>
        <div><label class="text-xs muted">Privacy</label>
          <select v-model="f.privacy_mode" class="input mt-1"><option value="self_hosted_only">Nur eigene Infrastruktur</option><option value="eu_only">Nur EU</option><option value="any">Beliebig</option></select></div>
        <div class="flex justify-end"><Btn type="submit">Anlegen</Btn></div>
      </form>
    </Modal>
  </div>
</template>
