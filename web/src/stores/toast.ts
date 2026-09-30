import { defineStore } from 'pinia'
export const useToast = defineStore('toast', {
  state: () => ({ items: [] as { id: number; text: string; tone: 'ok' | 'err' | 'info' }[] }),
  actions: {
    push(text: string, tone: 'ok' | 'err' | 'info' = 'info') {
      const id = Date.now() + Math.random()
      this.items.push({ id, text, tone })
      setTimeout(() => (this.items = this.items.filter((i) => i.id !== id)), 5000)
    },
    err(e: any) { this.push(e?.message || String(e), 'err') },
    ok(t: string) { this.push(t, 'ok') },
  },
})
