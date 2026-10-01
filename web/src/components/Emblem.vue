<script setup lang="ts">
// Fylgja-Emblem: Schutzgeist-Flamme mit Rune. size in px.
defineProps<{ size?: number; glow?: boolean }>()
</script>
<template>
  <svg :width="size ?? 28" :height="size ?? 28" viewBox="0 0 64 64" fill="none" aria-hidden="true" :style="glow ? 'filter: drop-shadow(0 0 10px var(--accent-glow))' : ''">
    <defs>
      <linearGradient id="emg" x1="32" y1="6" x2="32" y2="58" gradientUnits="userSpaceOnUse"><stop stop-color="#ff6b7a"/><stop offset="1" stop-color="#b71c2a"/></linearGradient>
    </defs>
    <path d="M32 5c-10 9-16 18-16 28a16 16 0 0 0 32 0c0-10-6-19-16-28z" stroke="url(#emg)" stroke-width="3.5" stroke-linejoin="round"/>
    <path d="M32 24v22M32 30l-7-6M32 30l7-6M32 38l-6-5M32 38l6-5" stroke="url(#emg)" stroke-width="3" stroke-linecap="round"/>
  </svg>
</template>
