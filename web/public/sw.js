// Minimaler Service Worker (PWA-Installierbarkeit). API-Aufrufe werden nie gecacht.
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()))
self.addEventListener('fetch', () => {})
