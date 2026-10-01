<script setup lang="ts">
import Emblem from './Emblem.vue'
</script>
<template>
  <div class="text-center py-12 px-6 muted text-sm flex flex-col items-center gap-3"><Emblem :size="30" class="opacity-30" /><div class="max-w-md"><slot /></div></div>
</template>
