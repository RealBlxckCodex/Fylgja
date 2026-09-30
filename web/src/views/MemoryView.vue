<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { get } from '@/lib/api'
import MemoryBrowser from '@/components/MemoryBrowser.vue'
const props = defineProps<{ id?: string }>()
const router = useRouter()
const dots = ref<any[]>([])
onMounted(async () => { dots.value = await get('/dots'); if (!props.id && dots.value[0]) router.replace(`/memory/${dots.value[0].dot.id}`) })
</script>
<template>
  <div class="space-y-4">
    <div class="flex items-center gap-3"><h1 class="text-xl font-semibold">Memory</h1>
      <select class="input !w-56" :value="id" aria-label="Fylgja" @change="router.push(`/memory/${($event.target as HTMLSelectElement).value}`)"><option v-for="d in dots" :key="d.dot.id" :value="d.dot.id">{{ d.dot.name }}</option></select></div>
    <MemoryBrowser v-if="id" :dot="id" />
  </div>
</template>
