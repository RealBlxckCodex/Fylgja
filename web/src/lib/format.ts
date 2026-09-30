export const eur = (micro?: number, digits = 2) =>
  ((micro ?? 0) / 1e6).toLocaleString('de-DE', { style: 'currency', currency: 'EUR', minimumFractionDigits: digits, maximumFractionDigits: digits })
export const pct = (v?: number) => `${Math.round((v ?? 0) * 100)} %`
export const num = (v?: number) => (v ?? 0).toLocaleString('de-DE')
export const short = (id?: string) => (id ? id.slice(0, 8) : '—')
export function ago(ts?: string | null) {
  if (!ts) return '—'
  const s = (Date.now() - new Date(ts).getTime()) / 1000
  if (s < 60) return `vor ${Math.max(1, Math.round(s))} s`
  if (s < 3600) return `vor ${Math.round(s / 60)} min`
  if (s < 86400) return `vor ${Math.round(s / 3600)} h`
  return new Date(ts).toLocaleDateString('de-DE')
}
export const dt = (ts?: string | null) => (ts ? new Date(ts).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' }) : '—')
export const ms = (v?: number) => (v == null ? '—' : v < 1000 ? `${Math.round(v)} ms` : `${(v / 1000).toFixed(1)} s`)
