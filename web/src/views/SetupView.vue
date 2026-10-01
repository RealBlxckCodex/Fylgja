<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { post } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import Btn from '@/components/Btn.vue'
import AuthLayout from './AuthLayout.vue'
const router = useRouter()
const auth = useAuth()
const f = ref({ workspace: 'Mein Workspace', name: '', email: '', password: '', dot_name: 'Hugin', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone })
const err = ref(''), busy = ref(false)
async function submit() { busy.value = true; err.value = ''; try { await post('/setup', f.value); await auth.load(); router.push('/') } catch (e: any) { err.value = e.message } finally { busy.value = false } }
</script>
<template>
  <AuthLayout title="Einrichten" subtitle="Einmalig: Workspace, dein Owner-Konto und deine erste Fylgja.">
    <form class="grid grid-cols-2 gap-3" @submit.prevent="submit">
      <div class="col-span-2"><label class="text-xs muted">Workspace</label><input v-model="f.workspace" class="input mt-1" required /></div>
      <div><label class="text-xs muted">Dein Name</label><input v-model="f.name" class="input mt-1" required /></div>
      <div><label class="text-xs muted">Name der Fylgja</label><input v-model="f.dot_name" class="input mt-1" required /></div>
      <div class="col-span-2"><label class="text-xs muted">E-Mail</label><input v-model="f.email" type="email" class="input mt-1" autocomplete="username" required /></div>
      <div class="col-span-2"><label class="text-xs muted">Passwort (mind. 12 Zeichen)</label><input v-model="f.password" type="password" minlength="12" class="input mt-1" autocomplete="new-password" required /></div>
      <div class="col-span-2"><label class="text-xs muted">Zeitzone</label><input v-model="f.timezone" class="input mt-1" /></div>
      <p v-if="err" class="col-span-2 text-sm" style="color: var(--err)">{{ err }}</p>
      <Btn type="submit" class="col-span-2" :loading="busy">Los geht’s</Btn>
    </form>
  </AuthLayout>
</template>
