<script setup lang="ts">
// Status nie nur über Farbe: Punkt/Icon + Text (Barrierefreiheit, Spec 18.1).
import { computed } from 'vue'
import Badge from './Badge.vue'
const props = defineProps<{ s?: string }>()
const map: Record<string, [string, any, string]> = {
  running: ['●', 'info', 'läuft'], queued: ['◷', 'muted', 'wartet'], waiting: ['⏸', 'warn', 'wartet auf Freigabe'],
  succeeded: ['✓', 'ok', 'fertig'], done: ['✓', 'ok', 'fertig'], failed: ['✕', 'err', 'fehlgeschlagen'], cancelled: ['⊘', 'muted', 'abgebrochen'],
  pending: ['◷', 'muted', 'offen'], ready: ['▶', 'info', 'bereit'], blocked: ['■', 'warn', 'blockiert'], needs_review: ['◎', 'warn', 'in Prüfung'],
  approved: ['✓', 'ok', 'freigegeben'], denied: ['✕', 'err', 'abgelehnt'], expired: ['⌛', 'muted', 'abgelaufen'],
  active: ['●', 'ok', 'aktiv'], paused: ['⏸', 'warn', 'pausiert'], archived: ['▫', 'muted', 'archiviert'],
  provisioning: ['◷', 'info', 'startet'], draining: ['⇣', 'warn', 'leert'], terminating: ['⇣', 'warn', 'beendet'], gone: ['▫', 'muted', 'weg'],
  closed: ['●', 'ok', 'ok'], open: ['✕', 'err', 'offen'], half_open: ['◐', 'warn', 'prüft'], loading: ['◷', 'info', 'lädt'], stopped: ['▫', 'muted', 'gestoppt'],
  planned: ['◇', 'muted', 'geplant'], accepted: ['✓', 'ok', 'angenommen'], rejected: ['✕', 'muted', 'abgelehnt'], sleeping: ['☾', 'muted', 'schläft'],
}
const v = computed(() => map[props.s ?? ''] ?? ['•', 'muted', props.s ?? '—'])
</script>
<template><Badge :tone="v[1]" :icon="v[0]" :class="s === 'running' ? 'aura' : ''">{{ v[2] }}</Badge></template>
