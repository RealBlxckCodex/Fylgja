<script setup lang="ts">
// „Denkt nach …“ / „N Schritte“ – aufklappbarer Denkverlauf wie bei Claude/ChatGPT.
import { ref, computed, watch } from 'vue'
import { ChevronRight, Sparkles, Globe, Brain, Terminal, FileText, Wrench, ShieldCheck, Check, X, Send, ListChecks, Network } from 'lucide-vue-next'
export interface Step { id: string; kind: 'thinking' | 'reasoning' | 'tool' | 'policy' | 'note'; tool?: string; cls?: string; args?: any; status?: 'running' | 'ok' | 'err' | 'ask' | 'denied'; preview?: string; text?: string; untrusted?: boolean; verdict?: string; reasons?: string[]; at: number; end?: number }
const props = withDefaults(defineProps<{ steps: Step[]; live?: boolean; elapsedMs?: number; defaultOpen?: boolean }>(), { defaultOpen: true })
const open = ref(props.defaultOpen)
const label: Record<string, string> = {
  'web.search': 'Websuche', 'web.fetch': 'Seite gelesen', 'memory.search': 'Gedächtnis durchsucht', 'memory.write': 'Erinnerung gespeichert', 'shell.run': 'Befehl ausgeführt',
  'fs.read': 'Datei gelesen', 'fs.write': 'Datei geschrieben', 'fs.list': 'Ordner gelistet', 'task.create': 'Aufgabe angelegt', 'task.list': 'Aufgaben geprüft', 'notify.owner': 'Nachricht an dich',
  'message.send': 'Nachricht gesendet', 'tools.search': 'Werkzeuge gesucht', 'subagent.spawn': 'Subagent gestartet', 'quarantine.extract': 'Dokument in Quarantäne gelesen', 'team.plan': 'Plan erstellt',
  'browser.navigate': 'Browser geöffnet', 'browser.click': 'Im Browser geklickt', 'browser.type': 'Im Browser getippt', 'browser.login': 'Angemeldet', 'dot.delegate': 'Aufgabe delegiert', 'schedule.create': 'Routine angelegt',
}
const iconFor = (t?: string) => !t ? Wrench : t.startsWith('web.') || t.startsWith('browser.') ? Globe : t.startsWith('memory.') ? Brain : t.startsWith('shell.') ? Terminal : t.startsWith('fs.') ? FileText
  : t.startsWith('message.') || t.startsWith('notify.') || t.startsWith('mail.') ? Send : t.startsWith('task.') || t.startsWith('schedule.') ? ListChecks : t.startsWith('team.') || t.startsWith('subagent.') || t.startsWith('dot.') ? Network : Wrench
const title = (s: Step) => label[s.tool || ''] || s.tool || 'Werkzeug'
function brief(a: any): string {
  if (!a || typeof a !== 'object') return ''
  const v = a.query ?? a.url ?? a.cmd ?? a.path ?? a.title ?? a.content ?? a.text ?? a.goal ?? Object.values(a)[0]
  return typeof v === 'string' ? v : JSON.stringify(v ?? '')
}
const secs = computed(() => ((props.elapsedMs ?? 0) / 1000))
const toolCount = computed(() => props.steps.filter((s) => s.kind === 'tool').length)
const summary = computed(() => {
  if (props.live) return null
  const t = toolCount.value
  const d = secs.value ? `${secs.value < 10 ? secs.value.toFixed(1) : Math.round(secs.value)} s` : ''
  return t ? `${t} Schritt${t > 1 ? 'e' : ''} ausgeführt${d ? ' · ' + d : ''}` : `Nachgedacht${d ? ' · ' + d : ''}`
})
const current = computed(() => { const last = [...props.steps].reverse().find((s) => s.status === 'running' || s.kind === 'thinking'); return last })
const liveText = computed(() => { const c = current.value; if (!c) return 'Denkt nach …'; if (c.kind === 'tool') return title(c) + ' …'; return 'Denkt nach …' })
const expanded = ref<Record<string, boolean>>({})
</script>
<template>
  <div class="my-1">
    <button class="group inline-flex items-center gap-2 text-[13.5px] rounded-lg -ml-1 px-1 py-0.5 hover:bg-[var(--panel-2)] focus-ring" :aria-expanded="open" @click="open = !open">
      <Sparkles class="size-4" :class="live ? 'accent' : 'muted'" aria-hidden="true" />
      <span v-if="live" class="thinking-text font-medium">{{ liveText }}</span>
      <span v-else class="muted group-hover:text-[var(--text)]">{{ summary }}</span>
      <span v-if="live && secs" class="muted mono text-xs">{{ Math.round(secs) }}s</span>
      <ChevronRight class="size-3.5 muted transition-transform" :class="open ? 'rotate-90' : ''" aria-hidden="true" />
    </button>
    <Transition enter-active-class="transition-all duration-200" enter-from-class="opacity-0 -translate-y-1" leave-active-class="transition-all duration-150" leave-to-class="opacity-0">
      <ol v-if="open" class="mt-2 ml-[7px] pl-5 border-l border-[var(--line-strong)] space-y-3.5">
        <li v-for="s in steps" :key="s.id" class="relative text-[13.5px]">
          <span class="absolute -left-[27px] top-[1px] size-[18px] rounded-full flex items-center justify-center" style="background: var(--bg-2); box-shadow: 0 0 0 1px var(--line-strong)">
            <span v-if="s.status === 'running'" class="size-2 rounded-full bg-[var(--accent-2)] aura" />
            <Check v-else-if="s.status === 'ok'" class="size-3" style="color: var(--ok)" />
            <X v-else-if="s.status === 'err' || s.status === 'denied'" class="size-3" style="color: var(--err)" />
            <ShieldCheck v-else-if="s.kind === 'policy' || s.status === 'ask'" class="size-3" style="color: var(--warn)" />
            <Sparkles v-else class="size-3 muted" />
          </span>
          <template v-if="s.kind === 'tool'">
            <button class="flex items-center gap-2 text-left w-full focus-ring rounded" @click="expanded[s.id] = !expanded[s.id]">
              <component :is="iconFor(s.tool)" class="size-3.5 muted shrink-0" aria-hidden="true" />
              <span class="font-medium">{{ title(s) }}</span>
              <span class="muted truncate flex-1 font-mono text-xs">{{ brief(s.args) }}</span>
              <span v-if="s.untrusted" class="text-[10px] rounded-full px-1.5 py-px" style="color: var(--warn); background: color-mix(in srgb, var(--warn) 14%, transparent)">untrusted</span>
              <ChevronRight class="size-3 muted transition-transform shrink-0" :class="expanded[s.id] ? 'rotate-90' : ''" />
            </button>
            <div v-if="expanded[s.id]" class="mt-2 rounded-lg p-3 text-xs space-y-2" style="background: var(--panel-2); box-shadow: inset 0 0 0 1px var(--line)">
              <div><div class="muted mb-1">Eingabe · {{ s.cls }}</div><pre class="mono whitespace-pre-wrap break-all">{{ JSON.stringify(s.args, null, 2) }}</pre></div>
              <div v-if="s.preview"><div class="muted mb-1">Ergebnis</div><pre class="mono whitespace-pre-wrap break-all max-h-48 overflow-auto">{{ s.preview }}</pre></div>
            </div>
          </template>
          <template v-else-if="s.kind === 'policy'">
            <div class="flex items-start gap-2"><span :style="{ color: s.verdict === 'allow' ? 'var(--ok)' : s.verdict === 'deny' ? 'var(--err)' : 'var(--warn)' }" class="font-medium">{{ s.verdict === 'allow' ? 'Erlaubt' : s.verdict === 'deny' ? 'Verweigert' : s.verdict === 'human_only' ? 'Bleibt beim Menschen' : 'Freigabe nötig' }}</span>
              <span class="muted text-xs mt-0.5">{{ (s.reasons || []).slice(0, 2).join(' · ') }}</span></div>
          </template>
          <template v-else-if="s.kind === 'reasoning'"><div class="muted whitespace-pre-wrap text-[13px] leading-relaxed">{{ s.text }}</div></template>
          <template v-else><span class="muted">{{ s.text || 'Überlegt, wie am besten vorzugehen ist …' }}</span></template>
        </li>
      </ol>
    </Transition>
  </div>
</template>
