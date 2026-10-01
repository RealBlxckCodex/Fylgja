<script setup lang="ts">
import Emblem from '@/components/Emblem.vue'
defineProps<{ title: string; subtitle: string }>()
</script>
<template>
  <div class="min-h-full grid lg:grid-cols-[1.1fr_1fr]">
    <div class="hidden lg:flex relative flex-col justify-between p-12 overflow-hidden">
      <div class="flex items-center gap-3"><Emblem :size="36" glow /><span class="font-semibold tracking-[0.18em] text-gradient text-lg">FYLGJA</span></div>
      <div class="relative">
        <div class="absolute -left-10 -top-24 opacity-[.07] pointer-events-none"><Emblem :size="460" /></div>
        <h2 class="relative text-5xl font-semibold leading-[1.08] tracking-tight">Dein Schutzgeist<br /><span class="text-gradient">arbeitet weiter,</span><br />wenn du es nicht tust.</h2>
        <ul class="relative mt-8 space-y-3 text-[15px] muted max-w-md">
          <li class="flex gap-3"><span class="accent">◆</span>Eigener Computer, Langzeitgedächtnis, proaktive Hintergrundarbeit</li>
          <li class="flex gap-3"><span class="accent">◆</span>Strikte Kontrolle: Freigaben, Regeln, lückenloses Audit</li>
          <li class="flex gap-3"><span class="accent">◆</span>Läuft auf deiner Hardware und deiner GPU-Flotte</li>
        </ul>
      </div>
      <div class="text-xs muted">Selbst gehostet · MIT-lizenziert</div>
    </div>
    <div class="flex items-center justify-center p-6">
      <div class="w-full max-w-sm rise">
        <div class="lg:hidden flex items-center gap-3 mb-8 justify-center"><Emblem :size="34" glow /><span class="font-semibold tracking-[0.18em] text-gradient text-lg">FYLGJA</span></div>
        <h1 class="text-2xl font-semibold tracking-tight">{{ title }}</h1>
        <p class="text-sm muted mt-1.5 mb-6">{{ subtitle }}</p>
        <div class="panel p-6 panel-glow"><slot /></div>
      </div>
    </div>
  </div>
</template>
