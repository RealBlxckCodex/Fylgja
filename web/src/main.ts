import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import './styles.css'

const saved = (() => { try { return localStorage.getItem('fylgja-theme') } catch { return null } })()
if (saved === 'light') document.documentElement.dataset.theme = 'light'

createApp(App).use(createPinia()).use(router).mount('#app')

if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {})
