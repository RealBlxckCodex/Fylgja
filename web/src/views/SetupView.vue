<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { post } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import Btn from '@/components/Btn.vue'
const router = useRouter()
const auth = useAuth()
const f = ref({ workspace: 'Mein Workspace', name: '', email: '', password: '', dot_name: 'Hugin', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone })
const err = ref('')
const busy = ref(false)
async function submit() {
  busy.value = true; err.value = ''
  try { await post('/setup', f.value); await auth.load(); router.push('/') } catch (e: any) { err.value = e.message } finally { busy.value = false }
}
</script>
<template>
  <div class="min-h-full flex items-center justify-center p-6">
    <form class="panel p-6 w-full max-w-md space-y-4" @submit.prevent="submit">
      <h1 class="text-xl font-semibold">Willkommen bei Fylgja</h1>
      <p class="text-sm muted">Einmalige Einrichtung: Workspace, dein Owner-Konto und deine erste Fylgja.</p>
      <div class="grid grid-cols-2 gap-3">
        <div class="col-span-2"><label class="text-xs muted">Workspace</label><input v-model="f.workspace" class="input mt-1" required /></div>
        <div><label class="text-xs muted">Dein Name</label><input v-model="f.name" class="input mt-1" required /></div>
        <div><label class="text-xs muted">Name der Fylgja</label><input v-model="f.dot_name" class="input mt-1" required /></div>
        <div class="col-span-2"><label class="text-xs muted">E-Mail</label><input v-model="f.email" type="email" class="input mt-1" autocomplete="username" required /></div>
        <div class="col-span-2"><label class="text-xs muted">Passwort (mind. 12 Zeichen)</label><input v-model="f.password" type="password" minlength="12" class="input mt-1" autocomplete="new-password" required /></div>
        <div class="col-span-2"><label class="text-xs muted">Zeitzone</label><input v-model="f.timezone" class="input mt-1" /></div>
      </div>
      <p v-if="err" class="text-sm" style="color: var(--err)">{{ err }}</p>
      <Btn type="submit" class="w-full" :loading="busy">Einrichten</Btn>
    </form>
  </div>
</template>
