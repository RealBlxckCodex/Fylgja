import { defineStore } from 'pinia'
import { get, post, passkeys } from '@/lib/api'

export interface Principal {
  user_id: string
  workspace_id: string
  email: string
  display_name: string
  role: string
}

export const useAuth = defineStore('auth', {
  state: () => ({
    principal: null as Principal | null,
    workspace: '',
    hasPasskey: false,
    steppedUp: false,
    loaded: false,
    stepUpOpen: false,
    stepUpResolve: null as ((ok: boolean) => void) | null,
  }),
  getters: {
    can: (s) => (action: 'read' | 'work' | 'manage' | 'own' | 'audit') => {
      const rank: Record<string, number> = { viewer: 1, auditor: 1, member: 2, admin: 3, owner: 4 }
      const r = rank[s.principal?.role ?? ''] ?? 0
      return { read: r >= 1, work: r >= 2, manage: r >= 3, own: r >= 4, audit: s.principal?.role === 'auditor' || r >= 3 }[action]
    },
  },
  actions: {
    async load() {
      try {
        const me = await get('/auth/me')
        this.principal = me.principal
        this.workspace = me.workspace_name
        this.hasPasskey = me.has_passkey
        this.steppedUp = me.stepped_up
      } catch {
        this.principal = null
      }
      this.loaded = true
    },
    async login(email: string, password: string) {
      await post('/auth/login', { email, password })
      await this.load()
    },
    async loginPasskey() {
      await passkeys.login()
      await this.load()
    },
    async logout() {
      await post('/auth/logout').catch(() => {})
      this.principal = null
    },
    // Öffnet den Step-up-Dialog und wartet auf das Ergebnis.
    requestStepUp(): Promise<boolean> {
      this.stepUpOpen = true
      return new Promise((res) => (this.stepUpResolve = res))
    },
    finishStepUp(ok: boolean) {
      this.stepUpOpen = false
      this.steppedUp = ok || this.steppedUp
      this.stepUpResolve?.(ok)
      this.stepUpResolve = null
    },
  },
})
