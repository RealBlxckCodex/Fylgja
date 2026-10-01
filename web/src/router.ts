import { createRouter, createWebHistory } from 'vue-router'
import { useAuth } from './stores/auth'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: () => import('./views/LoginView.vue'), meta: { public: true } },
    { path: '/setup', component: () => import('./views/SetupView.vue'), meta: { public: true } },
    { path: '/tg', component: () => import('./views/TgView.vue'), meta: { public: true } },
    { path: '/', component: () => import('./views/HomeView.vue') },
    { path: '/dots', component: () => import('./views/DotsView.vue') },
    { path: '/dots/:id', component: () => import('./views/DotView.vue'), props: true },
    { path: '/teams', component: () => import('./views/TeamsView.vue') },
    { path: '/teams/:id', component: () => import('./views/GraphView.vue'), props: true },
    { path: '/fleet', component: () => import('./views/FleetView.vue') },
    { path: '/computer/:id?', component: () => import('./views/ComputerView.vue'), props: true },
    { path: '/memory/:id?', component: () => import('./views/MemoryView.vue'), props: true },
    { path: '/learn', component: () => import('./views/LearnView.vue') },
    { path: '/rules', component: () => import('./views/RulesView.vue') },
    { path: '/approvals/:id?', component: () => import('./views/RulesView.vue'), props: (r) => ({ approval: r.params.id }) },
    { path: '/costs', component: () => import('./views/CostsView.vue') },
    { path: '/audit', component: () => import('./views/AuditView.vue') },
    { path: '/settings', component: () => import('./views/SettingsView.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuth()
  if (!auth.loaded) await auth.load()
  if (to.meta.public) return true
  if (!auth.principal) {
    const s = await fetch('/api/v1/setup/status').then((r) => r.json()).catch(() => ({}))
    return s.needs_setup ? '/setup' : { path: '/login', query: { next: to.fullPath } }
  }
  return true
})
