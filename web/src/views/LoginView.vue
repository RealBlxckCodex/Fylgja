<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuth } from '@/stores/auth'
import { passkeys } from '@/lib/api'
import Btn from '@/components/Btn.vue'
const auth = useAuth()
const router = useRouter()
const route = useRoute()
const email = ref('')
const password = ref('')
const err = ref('')
const busy = ref(false)
async function done() { router.push((route.query.next as string) || '/') }
async function submit() {
  busy.value = true; err.value = ''
  try { await auth.login(email.value, password.value); await done() } catch (e: any) { err.value = e.message } finally { busy.value = false }
}
async function passkey() {
  err.value = ''
  try { await auth.loginPasskey(); await done() } catch (e: any) { err.value = e.message || 'Passkey-Anmeldung fehlgeschlagen' }
}
</script>
<template>
  <div class="min-h-full flex items-center justify-center p-6">
    <div class="w-full max-w-sm">
      <div class="flex items-center gap-3 mb-8 justify-center">
        <svg viewBox="0 0 64 64" class="size-10"><path d="M32 10c-9 8-14 16-14 24a14 14 0 0 0 28 0c0-8-5-16-14-24z" fill="none" stroke="#B71C2A" stroke-width="4"/><circle cx="32" cy="36" r="5" fill="#B71C2A"/></svg>
        <h1 class="text-2xl font-semibold tracking-wide">Fylgja</h1>
      </div>
      <form class="panel p-6 space-y-4" @submit.prevent="submit">
        <div><label class="text-xs muted" for="email">E-Mail</label><input id="email" v-model="email" class="input mt-1" type="email" autocomplete="username" required /></div>
        <div><label class="text-xs muted" for="pw">Passwort</label><input id="pw" v-model="password" class="input mt-1" type="password" autocomplete="current-password" required /></div>
        <p v-if="err" class="text-sm" style="color: var(--err)" role="alert">{{ err }}</p>
        <Btn type="submit" class="w-full" :loading="busy">Anmelden</Btn>
        <Btn v-if="passkeys.supported()" variant="ghost" class="w-full" @click="passkey">🔐 Mit Passkey anmelden</Btn>
      </form>
    </div>
  </div>
</template>
