// SSE-Abonnement mit automatischem Reconnect (EventSource sendet Last-Event-ID selbst).
import { onBeforeUnmount } from 'vue'

export type Handler = (topic: string, data: any) => void

export function subscribe(topics: string[], handler: Handler): () => void {
  const es = new EventSource('/api/v1/stream?topics=' + encodeURIComponent(topics.join(',')), { withCredentials: true })
  const listen = (t: string) =>
    es.addEventListener(t, (e) => {
      try {
        handler(t, JSON.parse((e as MessageEvent).data))
      } catch {
        /* ignorieren */
      }
    })
  topics.forEach(listen)
  return () => es.close()
}

// useStream: im Setup einer Komponente benutzen; schließt automatisch.
export function useStream(topics: () => string[], handler: Handler) {
  let close: (() => void) | null = null
  const start = () => {
    close?.()
    const t = topics().filter(Boolean)
    if (t.length) close = subscribe(t, handler)
  }
  start()
  onBeforeUnmount(() => close?.())
  return { restart: start, stop: () => close?.() }
}
