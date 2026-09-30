<script setup lang="ts">
import { useRoute, useRouter } from 'vue-router'
import { computed } from 'vue'
import AppShell from './components/AppShell.vue'
import StepUp from './components/StepUp.vue'
import { useAuth } from './stores/auth'
import { useToast } from './stores/toast'
import { onStepUp, onUnauthorized } from './lib/api'
const route = useRoute()
const router = useRouter()
const auth = useAuth()
const toast = useToast()
onStepUp(() => auth.requestStepUp())
onUnauthorized(() => { auth.principal = null; router.push('/login') })
const bare = computed(() => route.meta.public)
</script>
<template>
  <RouterView v-if="bare" />
  <AppShell v-else><RouterView :key="route.fullPath" /></AppShell>
  <StepUp />
  <div class="fixed bottom-4 right-4 z-50 space-y-2 w-80" aria-live="polite">
    <div v-for="t in toast.items" :key="t.id" class="panel px-4 py-3 text-sm shadow-lg"
      :style="{ borderColor: t.tone === 'err' ? 'var(--err)' : t.tone === 'ok' ? 'var(--ok)' : 'var(--line)' }">
      {{ t.tone === 'err' ? '⚠ ' : t.tone === 'ok' ? '✓ ' : '' }}{{ t.text }}
    </div>
  </div>
</template>
