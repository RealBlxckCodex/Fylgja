<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Bot, Server, Cpu, Box } from 'lucide-vue-next'

const props = defineProps<{ agents: any[]; deployments: any[] }>()

const root = ref<HTMLElement>()
const nodeEls = new Map<string, HTMLElement>()
const edgesPx = ref<{ id: string; d: string; w: number; kind: 'ah' | 'hd'; hot: boolean; bad: boolean; from: string; to: string }[]>([])
const size = ref({ w: 0, h: 0 })
const hover = ref<string | null>(null)

function reg(key: string) { return (el: any) => { if (el) nodeEls.set(key, el as HTMLElement); else nodeEls.delete(key) } }

const hosts = computed(() => {
  const m = new Map<string, { name: string; agents: number; calls: number }>()
  for (const a of props.agents) {
    const n = a.sandbox_host || 'ohne Sandbox'
    const h = m.get(n) ?? { name: n, agents: 0, calls: 0 }
    h.agents++
    for (const c of Object.values(a.deployments ?? {})) h.calls += c as number
    m.set(n, h)
  }
  return [...m.values()]
})
const links = computed(() => {
  const out: { from: string; to: string; kind: 'ah' | 'hd'; n: number }[] = []
  const hd = new Map<string, number>()
  for (const a of props.agents) {
    const h = a.sandbox_host || 'ohne Sandbox'
    out.push({ from: 'a:' + a.run_id, to: 'h:' + h, kind: 'ah', n: 1 })
    for (const [d, n] of Object.entries(a.deployments ?? {})) hd.set(h + '\u0000' + d, (hd.get(h + '\u0000' + d) ?? 0) + (n as number))
  }
  for (const [k, n] of hd) { const [h, d] = k.split('\u0000'); out.push({ from: 'h:' + h, to: 'd:' + d, kind: 'hd', n }) }
  return out
})

function measure() {
  const r = root.value; if (!r) return
  const rb = r.getBoundingClientRect()
  size.value = { w: rb.width, h: rb.height }
  const depBad = new Map(props.deployments.map((d) => [d.name, d.breaker !== 'closed']))
  edgesPx.value = links.value.flatMap((l) => {
    const a = nodeEls.get(l.from), b = nodeEls.get(l.to)
    if (!a || !b) return []
    const ab = a.getBoundingClientRect(), bb = b.getBoundingClientRect()
    const x1 = ab.right - rb.left, y1 = ab.top + ab.height / 2 - rb.top
    const x2 = bb.left - rb.left, y2 = bb.top + bb.height / 2 - rb.top
    const dx = Math.max(40, (x2 - x1) * 0.5)
    return [{ id: l.from + '>' + l.to, from: l.from, to: l.to, kind: l.kind, d: `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`,
      w: l.kind === 'ah' ? 1.5 : Math.min(6, 1.5 + Math.log2(1 + l.n)), hot: l.n > 0, bad: l.kind === 'hd' && !!depBad.get(l.to.slice(2)) }]
  })
}
let ro: ResizeObserver | undefined
onMounted(() => { ro = new ResizeObserver(() => measure()); if (root.value) ro.observe(root.value); nextTick(measure) })
onBeforeUnmount(() => ro?.disconnect())
watch(() => [props.agents, props.deployments], () => nextTick(measure), { deep: true })

function related(id: string) {
  if (!hover.value) return true
  if (id === hover.value) return true
  const hv = hover.value
  const rel = new Set<string>([hv])
  for (let i = 0; i < 2; i++) for (const l of links.value) { if (rel.has(l.from) && hv.startsWith('a:') || rel.has(l.from) && hv.startsWith('h:')) rel.add(l.to); if (rel.has(l.to) && (hv.startsWith('d:') || hv.startsWith('h:'))) rel.add(l.from) }
  return rel.has(id)
}
const edgeOn = (e: { from: string; to: string }) => !hover.value || related(e.from) && related(e.to)
const load = (d: any) => d.max_concurrency ? Math.min(100, Math.round(d.inflight / d.max_concurrency * 100)) : 0
const depTone = (d: any) => d.breaker !== 'closed' ? 'err' : d.state === 'ready' ? 'ok' : 'warn'
</script>

<template>
  <div ref="root" class="ft">
    <svg class="ft-svg" :width="size.w" :height="size.h" :viewBox="`0 0 ${size.w} ${size.h}`">
      <defs>
        <linearGradient id="ftg" x1="0" x2="1"><stop offset="0" stop-color="#c8202f" stop-opacity=".25" /><stop offset="1" stop-color="#ff4a5c" stop-opacity=".9" /></linearGradient>
        <linearGradient id="ftg2" x1="0" x2="1"><stop offset="0" stop-color="#ffffff" stop-opacity=".12" /><stop offset="1" stop-color="#ffffff" stop-opacity=".3" /></linearGradient>
      </defs>
      <g v-for="e in edgesPx" :key="e.id" :class="{ dim: !edgeOn(e) }" class="ft-edge">
        <path :d="e.d" fill="none" :stroke="e.kind === 'ah' ? 'rgba(255,255,255,.22)' : e.bad ? '#f05a28' : 'rgba(255,74,92,.55)'" :stroke-width="e.w" stroke-linecap="round" />
        <path v-if="e.kind === 'hd' && e.hot" :d="e.d" fill="none" class="ft-flow" :stroke="e.bad ? '#f05a28' : '#ff8a96'" :stroke-width="Math.max(1.5, e.w / 2)" stroke-linecap="round" />
      </g>
    </svg>

    <div class="ft-col">
      <div class="ft-h"><Bot :size="13" /> Agenten <span>{{ agents.length }}</span></div>
      <div v-for="a in agents" :key="a.run_id" :ref="reg('a:' + a.run_id)" class="ft-node" :class="{ dim: !related('a:' + a.run_id) }"
        @mouseenter="hover = 'a:' + a.run_id" @mouseleave="hover = null">
        <span class="ft-dot" :class="a.status === 'running' ? 'on' : ''" />
        <div class="min-w-0"><div class="ft-t">{{ a.dot_name }}</div><div class="ft-s">{{ a.kind }} · {{ a.status }}</div></div>
      </div>
      <div v-if="!agents.length" class="ft-empty">Keine aktiven Läufe</div>
    </div>

    <div class="ft-col">
      <div class="ft-h"><Box :size="13" /> Sandbox-Hosts <span>{{ hosts.length }}</span></div>
      <div v-for="h in hosts" :key="h.name" :ref="reg('h:' + h.name)" class="ft-node" :class="{ dim: !related('h:' + h.name) }"
        @mouseenter="hover = 'h:' + h.name" @mouseleave="hover = null">
        <Server :size="15" class="ft-ic" />
        <div class="min-w-0"><div class="ft-t">{{ h.name }}</div><div class="ft-s">{{ h.agents }} Agent{{ h.agents === 1 ? '' : 'en' }} · {{ h.calls }} Calls</div></div>
      </div>
      <div v-if="!hosts.length" class="ft-empty">—</div>
    </div>

    <div class="ft-col">
      <div class="ft-h"><Cpu :size="13" /> Modell-Deployments <span>{{ deployments.length }}</span></div>
      <div v-for="d in deployments" :key="d.name" :ref="reg('d:' + d.name)" class="ft-node ft-dep" :class="[{ dim: !related('d:' + d.name) }, 'tone-' + depTone(d)]"
        @mouseenter="hover = 'd:' + d.name" @mouseleave="hover = null">
        <div class="min-w-0 flex-1">
          <div class="flex items-center justify-between gap-2"><div class="ft-t">{{ d.name }}</div><span class="ft-pill" :class="'tone-' + depTone(d)">{{ d.breaker !== 'closed' ? 'breaker ' + d.breaker : d.state }}</span></div>
          <div class="ft-bar"><i :style="{ width: load(d) + '%' }" /></div>
          <div class="ft-s">{{ d.inflight }}/{{ d.max_concurrency }} parallel · {{ load(d) }}%</div>
        </div>
      </div>
      <div v-if="!deployments.length" class="ft-empty">Keine Deployments</div>
    </div>
  </div>
</template>

<style scoped>
.ft { position: relative; display: grid; grid-template-columns: 1fr 1fr 1.15fr; gap: 0 clamp(36px, 7vw, 110px); padding: 6px 4px 14px; min-height: 240px;
  background: radial-gradient(circle at 1px 1px, rgba(255,255,255,.06) 1px, transparent 0) 0 0/22px 22px; border-radius: 14px; }
.ft-svg { position: absolute; inset: 0; pointer-events: none; overflow: visible; }
.ft-col { position: relative; z-index: 1; display: flex; flex-direction: column; gap: 10px; align-self: start; }
.ft-h { display: flex; align-items: center; gap: 6px; font-size: 11px; letter-spacing: .08em; text-transform: uppercase; color: #8b8b97; padding: 6px 4px; }
.ft-h span { margin-left: auto; font-variant-numeric: tabular-nums; background: rgba(255,255,255,.06); padding: 0 7px; border-radius: 99px; }
.ft-node { display: flex; align-items: center; gap: 10px; padding: 10px 12px; border-radius: 12px; cursor: default;
  background: linear-gradient(180deg, rgba(255,255,255,.055), rgba(255,255,255,.025)); border: 1px solid rgba(255,255,255,.09);
  box-shadow: 0 6px 20px -10px rgba(0,0,0,.8), inset 0 1px 0 rgba(255,255,255,.05); backdrop-filter: blur(6px); transition: opacity .2s, border-color .2s, transform .2s; }
.ft-node:hover { border-color: rgba(255,74,92,.5); transform: translateY(-1px); }
.dim { opacity: .25; }
.ft-edge { transition: opacity .2s; }
.ft-t { font-size: 13px; font-weight: 500; color: #ececf1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ft-s { font-size: 11px; color: #8b8b97; margin-top: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ft-ic { color: #ff8a96; flex: none; }
.ft-dot { width: 8px; height: 8px; border-radius: 50%; background: #55555f; flex: none; }
.ft-dot.on { background: #3fb68b; box-shadow: 0 0 0 4px rgba(63,182,139,.18); animation: ftp 2s infinite; }
@keyframes ftp { 50% { box-shadow: 0 0 0 7px rgba(63,182,139,0); } }
.ft-dep { border-left: 3px solid #3fb68b; }
.ft-dep.tone-warn { border-left-color: #e6b422; } .ft-dep.tone-err { border-left-color: #f05a28; }
.ft-pill { font-size: 10px; padding: 1px 8px; border-radius: 99px; background: rgba(63,182,139,.15); color: #6fd3ae; white-space: nowrap; }
.ft-pill.tone-warn { background: rgba(230,180,34,.15); color: #f0c64f; } .ft-pill.tone-err { background: rgba(240,90,40,.16); color: #ff8f66; }
.ft-bar { height: 4px; border-radius: 4px; background: rgba(255,255,255,.08); margin: 7px 0 5px; overflow: hidden; }
.ft-bar i { display: block; height: 100%; border-radius: 4px; background: linear-gradient(90deg, #c8202f, #ff4a5c); transition: width .4s; }
.ft-empty { font-size: 12px; color: #6b6b76; padding: 12px; border: 1px dashed rgba(255,255,255,.1); border-radius: 12px; text-align: center; }
.ft-flow { stroke-dasharray: 2 14; animation: ftd 1.1s linear infinite; }
@keyframes ftd { to { stroke-dashoffset: -16; } }
@media (max-width: 760px) { .ft { grid-template-columns: 1fr; gap: 20px; } .ft-svg { display: none; } }
</style>
