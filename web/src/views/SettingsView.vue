<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { get, post, put, passkeys } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import { useToast } from '@/stores/toast'
import Card from '@/components/Card.vue'
import Btn from '@/components/Btn.vue'
import Badge from '@/components/Badge.vue'
import Status from '@/components/Status.vue'
import Tabs from '@/components/Tabs.vue'
const auth = useAuth()
const toast = useToast()
const tab = ref('security')
const skills = ref<any[]>([])
const tools = ref<any[]>([])
const channels = ref<any>({})
const vc = ref<any>(null)
const vset = ref({ voice: '', language: '', speed: 1 })
const testText = ref('Hallo, ich bin deine Fylgja. So klinge ich.')
const testing = ref(false)
async function loadVoice() {
  vc.value = await get('/voice/config')
  vset.value = { voice: vc.value.settings.voice, language: vc.value.settings.language || '', speed: vc.value.settings.speed || 1 }
}
async function saveVoice() {
  try { await put('/voice/settings', { voice: vset.value.voice, language: vset.value.language, speed: Number(vset.value.speed) }); toast.ok('Gespeichert'); loadVoice() } catch (e) { toast.err(e) }
}
async function testSpeak() {
  testing.value = true
  try {
    const r = await fetch('/api/v1/voice/test/speak', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'fylgja', 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ text: testText.value }) })
    if (!r.ok) throw new Error((await r.json().catch(() => ({}))).detail || 'Sprachausgabe fehlgeschlagen')
    const a = new Audio(URL.createObjectURL(await r.blob())); await a.play()
  } catch (e) { toast.err(e) } finally { testing.value = false }
}
const ttsVoices = computed(() => Object.entries(vc.value?.voices || {}) as [string, string[]][])
const reg = ref<any>({ configured: false, skills: [], errors: [] })
const token = ref<any>(null)
const tf = ref({ name: 'fyctl', scopes: '*', ttl_days: 90 })
async function load() { ;[skills.value, tools.value, channels.value, reg.value] = await Promise.all([get('/skills'), get('/tools'), get('/channels'), get('/skills/registry')]) }
async function install(r: any) {
  try { await post('/skills/registry/install', { registry: r.registry, name: r.name, version: r.version }); toast.ok('Als Entwurf installiert – bitte prüfen und aktivieren'); load() } catch (e) { toast.err(e) }
}
onMounted(() => { load().catch(toast.err); loadVoice().catch(() => {}) })
async function addPasskey() { try { await passkeys.register(); toast.ok('Passkey registriert'); auth.load() } catch (e) { toast.err(e) } }
async function createToken() {
  try { token.value = await post('/auth/tokens', { name: tf.value.name, scopes: tf.value.scopes.split(',').map((s) => s.trim()).filter(Boolean), ttl_days: Number(tf.value.ttl_days) }) } catch (e) { toast.err(e) }
}
async function activate(s: any) {
  try { await post(`/skills/${s.id}/activate`); toast.ok('Skill signiert und aktiviert'); load() } catch (e: any) { toast.err(e.body?.findings?.length ? `Scanner: ${e.body.findings.map((f: any) => f.rule).join(', ')}` : e) }
}
async function disable(s: any) { try { await post(`/skills/${s.id}/disable`); load() } catch (e) { toast.err(e) } }
const classTone: Record<string, any> = { read: 'ok', write_internal: 'ok', compute: 'info', write_external: 'warn', communicate: 'warn', destructive: 'err', spend: 'err', credential: 'accent', laptop: 'accent' }
</script>
<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">Einstellungen</h1>
    <Tabs v-model="tab" :tabs="[{ id: 'security', label: 'Sicherheit' }, { id: 'skills', label: 'Skills' }, { id: 'tools', label: 'Werkzeuge' }, { id: 'voice', label: 'Sprache' }, { id: 'channels', label: 'Kanäle' }]" />
    <div v-if="tab === 'security'" class="grid gap-4 lg:grid-cols-2">
      <Card title="Passkeys" subtitle="Für Login und Step-up bei kritischen Aktionen">
        <p class="text-sm mb-3">Status: <Badge :tone="auth.hasPasskey ? 'ok' : 'warn'">{{ auth.hasPasskey ? 'registriert' : 'keiner' }}</Badge></p>
        <Btn :disabled="!passkeys.supported()" @click="addPasskey">🔐 Passkey hinzufügen</Btn>
      </Card>
      <Card title="API-Tokens" subtitle="Für fyctl, Automatisierung, Router-Gateway">
        <form class="grid grid-cols-3 gap-2 text-sm" @submit.prevent="createToken">
          <input v-model="tf.name" class="input" placeholder="Name" /><input v-model="tf.scopes" class="input mono" placeholder="Scopes (z. B. *, tasks:*, stepup)" />
          <input v-model.number="tf.ttl_days" type="number" class="input" placeholder="Tage" /><Btn type="submit" class="col-span-3">Token erzeugen</Btn>
        </form>
        <div v-if="token" class="mt-3 panel panel-2 p-3 text-xs"><div class="muted">Nur jetzt sichtbar:</div><code class="mono break-all">{{ token.token }}</code></div>
      </Card>
    </div>
    <div v-else-if="tab === 'skills'" class="space-y-4"><Card title="Skills" subtitle="Aktivierung nur nach Scanner-Prüfung und Signatur; Änderungen danach deaktivieren den Skill" flush>
      <table class="dense"><thead><tr><th>Skill</th><th>Herkunft</th><th>Status</th><th>Manifest</th><th>Scanner</th><th /></tr></thead>
        <tbody><tr v-for="s in skills" :key="s.id"><td><div class="font-medium">{{ s.name }} <span class="muted text-xs">v{{ s.version }}</span></div><div class="text-xs muted">{{ s.description }}</div></td>
          <td>{{ s.origin }}</td><td><Status :s="s.status === 'active' ? 'active' : s.status === 'draft' ? 'pending' : 'stopped'" /> <Badge v-if="s.signed" tone="ok">signiert</Badge></td>
          <td class="mono text-[11px]">{{ JSON.stringify(s.manifest) }}</td>
          <td><Badge v-if="s.findings?.length" tone="err" icon="⚠">{{ s.findings.length }} Funde</Badge><Badge v-else tone="ok">sauber</Badge></td>
          <td class="whitespace-nowrap"><Btn v-if="s.status !== 'active'" size="sm" @click="activate(s)">Prüfen & aktivieren</Btn><Btn v-else size="sm" variant="ghost" @click="disable(s)">Deaktivieren</Btn></td></tr></tbody></table>
    </Card>
    <Card title="Registry" subtitle="Skills aus Registries, deren Publisher du als vertrauenswürdig konfiguriert hast. Installation legt nur einen Entwurf an." flush>
      <p v-if="!reg.configured" class="p-5 text-sm muted">Keine Registry konfiguriert. In der Konfiguration unter <span class="mono">skills.registries</span> und <span class="mono">skills.trusted_publishers</span> eintragen.</p>
      <table v-else class="dense"><thead><tr><th>Skill</th><th>Publisher</th><th>Prüfung</th><th /></tr></thead>
        <tbody><tr v-for="r in reg.skills" :key="r.registry + r.name + r.version"><td><div class="font-medium">{{ r.name }} <span class="muted text-xs">v{{ r.version }}</span></div><div class="text-xs muted">{{ r.description }}</div></td>
          <td>{{ r.publisher }}</td><td><Badge v-if="r.verified" tone="ok">Signatur gültig</Badge><Badge v-else tone="err">{{ r.problem }}</Badge></td>
          <td><Btn v-if="r.verified" size="sm" @click="install(r)">Installieren</Btn></td></tr></tbody></table>
      <p v-if="reg.errors?.length" class="px-5 pb-4 text-xs" style="color: var(--warn)">{{ reg.errors.join(' · ') }}</p>
    </Card></div>
    <div v-else-if="tab === 'voice'" class="space-y-4">
      <Card title="Aurora" subtitle="Selbst gehostete Sprach-Engine für Spracherkennung und Sprachausgabe">
        <template v-if="!vc?.aurora">
          <p class="text-sm muted">Nicht konfiguriert. Setze <span class="mono">FYLGJA_AURORA_URL</span> (z. B. <span class="mono">http://aurora:11435</span>), optional <span class="mono">FYLGJA_AURORA_API_KEY</span>, und starte Fylgja neu. Fylgja legt dann die Modelle <span class="mono">stt</span> und <span class="mono">tts</span> selbst an. Alternativ kannst du die logischen Modelle <span class="mono">stt</span> und <span class="mono">tts</span> mit einem beliebigen OpenAI-kompatiblen Audio-Server belegen.</p>
          <p class="text-sm mt-3"><Badge :tone="vc?.stt ? 'ok' : 'muted'">Spracherkennung {{ vc?.stt ? 'bereit' : 'fehlt' }}</Badge> <Badge :tone="vc?.tts ? 'ok' : 'muted'">Sprachausgabe {{ vc?.tts ? 'bereit' : 'fehlt' }}</Badge></p>
        </template>
        <template v-else>
          <div class="flex items-center gap-2 flex-wrap text-sm">
            <Badge :tone="vc.aurora.ok ? 'ok' : 'err'">{{ vc.aurora.ok ? 'erreichbar' : 'nicht erreichbar' }}</Badge>
            <span class="mono text-xs">{{ vc.aurora.endpoint }}</span>
            <span v-if="vc.aurora.version" class="muted text-xs">Version {{ vc.aurora.version }}{{ vc.aurora.latency_ms ? " · " + vc.aurora.latency_ms + " ms" : "" }}</span>
          </div>
          <p v-if="vc.aurora.error" class="text-sm mt-2" style="color: var(--err)">{{ vc.aurora.error }}</p>
          <table v-if="vc.aurora.models.length" class="dense mt-4"><thead><tr><th>Modell</th><th>Typ</th><th>Backend</th><th>Geladen</th><th>Genutzt für</th></tr></thead>
            <tbody><tr v-for="m in vc.aurora.models" :key="m.id"><td class="mono text-xs">{{ m.id }}</td><td>{{ m.type === 'tts' ? 'Sprachausgabe' : m.type === 'stt' ? 'Spracherkennung' : m.type }}</td><td>{{ m.backend }}</td><td>{{ m.loaded ? '✓' : '' }}</td>
              <td><Badge v-if="m.id === vc.models.tts || m.id === vc.models.stt" tone="accent">aktiv</Badge></td></tr></tbody></table>
          <p v-else-if="vc.aurora.ok" class="text-sm muted mt-3">Aurora läuft, hat aber noch keine Modelle geladen. Auf dem Aurora-Server: <span class="mono">aurora pull kokoro-v1</span> und <span class="mono">aurora pull whisper-turbo</span>.</p>
          <p class="text-xs muted mt-3">Modelle werden auf dem Aurora-Server verwaltet (<span class="mono">aurora pull</span>, <span class="mono">aurora rm</span>); die aktiven Modelle stehen in der Fylgja-Konfiguration unter <span class="mono">voice.aurora</span>.</p>
        </template>
      </Card>
      <Card v-if="vc?.tts || vc?.stt" title="Stimme und Sprache" subtitle="Gilt für alle Fylgjur dieses Workspaces">
        <div class="grid gap-3 sm:grid-cols-3 max-w-2xl">
          <label class="text-sm">Stimme
            <select v-if="ttsVoices.length" v-model="vset.voice" class="input mt-1"><optgroup v-for="[model, list] in ttsVoices" :key="model" :label="model"><option v-for="v in list" :key="v" :value="v">{{ v }}</option></optgroup></select>
            <input v-else v-model="vset.voice" class="input mt-1" placeholder="alloy" />
          </label>
          <label class="text-sm">Sprache (Erkennung)
            <select v-if="vc?.aurora?.languages?.length" v-model="vset.language" class="input mt-1"><option value="">automatisch</option><option v-for="l in vc.aurora.languages" :key="l.code" :value="l.code">{{ l.native_name || l.name }} ({{ l.code }})</option></select>
            <input v-else v-model="vset.language" class="input mt-1" placeholder="leer = automatisch, z. B. de" />
          </label>
          <label class="text-sm">Tempo {{ Number(vset.speed).toFixed(2) }}×
            <input v-model="vset.speed" type="range" min="0.5" max="2" step="0.05" class="w-full mt-2" />
          </label>
        </div>
        <div class="flex items-center gap-2 mt-4"><Btn @click="saveVoice">Speichern</Btn></div>
        <div v-if="vc?.tts" class="mt-5 pt-4 border-t border-[var(--line)] max-w-2xl">
          <label class="text-sm" for="vt">Probe anhören (nach dem Speichern)</label>
          <div class="flex gap-2 mt-1"><input id="vt" v-model="testText" class="input flex-1" maxlength="300" /><Btn variant="ghost" :disabled="testing" @click="testSpeak">{{ testing ? 'Spricht …' : 'Abspielen' }}</Btn></div>
        </div>
      </Card>
    </div>
    <Card v-else-if="tab === 'tools'" title="Werkzeuge" :subtitle="`${tools.length} registriert · Klasse bestimmt die Policy`" flush>
      <table class="dense"><thead><tr><th>Tool</th><th>Klasse</th><th>Quelle</th><th>Basis-Set</th><th>Beschreibung</th></tr></thead>
        <tbody><tr v-for="t in tools" :key="t.name"><td class="mono text-xs">{{ t.name }}</td><td><Badge :tone="classTone[t.class]">{{ t.class }}</Badge></td><td class="muted text-xs">{{ t.source }}</td><td>{{ t.base ? '✓' : '' }}</td><td class="text-xs muted">{{ t.description }}</td></tr></tbody></table>
    </Card>
    <Card v-else title="Kanäle" subtitle="Bots werden über FYLGJA_TELEGRAM_TOKEN / FYLGJA_DISCORD_TOKEN konfiguriert">
      <div v-for="(h, k) in channels" :key="k" class="flex gap-3 items-center text-sm py-1"><Status :s="h.ok ? 'active' : 'failed'" /><span class="mono">{{ k }}</span><span class="muted">{{ h.detail }}</span></div>
      <p v-if="!Object.keys(channels).length" class="text-sm muted">Keine Kanäle aktiv.</p>
    </Card>
  </div>
</template>
