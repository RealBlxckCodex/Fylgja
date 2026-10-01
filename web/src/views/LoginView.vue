<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuth } from '@/stores/auth'
import { passkeys } from '@/lib/api'
import Btn from '@/components/Btn.vue'
import AuthLayout from './AuthLayout.vue'
const auth = useAuth()
const router = useRouter()
const route = useRoute()
const email = ref(''), password = ref(''), err = ref(''), busy = ref(false)
const done = () => router.push((route.query.next as string) || '/')
async function submit() { busy.value = true; err.value = ''; try { await auth.login(email.value, password.value); await done() } catch (e: any) { err.value = e.message } finally { busy.value = false } }
async function passkey() { err.value = ''; try { await auth.loginPasskey(); await done() } catch (e: any) { err.value = e.message || 'Passkey-Anmeldung fehlgeschlagen' } }
</script>
<template>
  <AuthLayout title="Willkommen zurück" subtitle="Melde dich an, um deine Fylgjur zu sehen.">
    <form class="space-y-4" @submit.prevent="submit">
      <div><label class="text-xs muted" for="email">E-Mail</label><input id="email" v-model="email" class="input mt-1.5" type="email" autocomplete="username" required /></div>
      <div><label class="text-xs muted" for="pw">Passwort</label><input id="pw" v-model="password" class="input mt-1.5" type="password" autocomplete="current-password" required /></div>
      <p v-if="err" class="text-sm" style="color: var(--err)" role="alert">{{ err }}</p>
      <Btn type="submit" class="w-full" :loading="busy">Anmelden</Btn>
      <Btn v-if="passkeys.supported()" variant="ghost" class="w-full" @click="passkey">🔐 Mit Passkey anmelden</Btn>
    </form>
  </AuthLayout>
</template>
