<script setup lang="ts">
import { ref } from 'vue'
import Modal from './Modal.vue'
import Btn from './Btn.vue'
import { useAuth } from '@/stores/auth'
import { post, passkeys } from '@/lib/api'
const auth = useAuth()
const pw = ref('')
const err = ref('')
const busy = ref(false)
async function withPasskey() {
  busy.value = true; err.value = ''
  try { await passkeys.stepUp(); auth.finishStepUp(true) } catch (e: any) { err.value = e.message || 'Passkey fehlgeschlagen' } finally { busy.value = false }
}
async function withPassword() {
  busy.value = true; err.value = ''
  try { await post('/auth/stepup/password', { password: pw.value }); pw.value = ''; auth.finishStepUp(true) } catch (e: any) { err.value = e.message } finally { busy.value = false }
}
</script>
<template>
  <Modal :open="auth.stepUpOpen" title="Bestätigung erforderlich" @close="auth.finishStepUp(false)">
    <p class="text-sm muted mb-4">Diese Aktion ist sicherheitskritisch (z. B. Zahlung, mehr Autonomie, breite Regel, Flotte). Bitte bestätige, dass du es bist.</p>
    <div class="space-y-3">
      <Btn v-if="auth.hasPasskey && passkeys.supported()" class="w-full" :loading="busy" @click="withPasskey">🔐 Mit Passkey bestätigen</Btn>
      <form class="flex gap-2" @submit.prevent="withPassword">
        <label class="sr-only" for="stepup-pw">Passwort</label>
        <input id="stepup-pw" v-model="pw" type="password" class="input" placeholder="Passwort" autocomplete="current-password" />
        <Btn type="submit" variant="ghost" :loading="busy">Bestätigen</Btn>
      </form>
      <p v-if="err" class="text-sm" style="color: var(--err)">{{ err }}</p>
    </div>
  </Modal>
</template>
