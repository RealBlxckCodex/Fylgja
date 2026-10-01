<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { get, post, passkeys } from '@/lib/api'
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
const reg = ref<any>({ configured: false, skills: [], errors: [] })
const token = ref<any>(null)
const tf = ref({ name: 'fyctl', scopes: '*', ttl_days: 90 })
async function load() { ;[skills.value, tools.value, channels.value, reg.value] = await Promise.all([get('/skills'), get('/tools'), get('/channels'), get('/skills/registry')]) }
async function install(r: any) {
  try { await post('/skills/registry/install', { registry: r.registry, name: r.name, version: r.version }); toast.ok('Als Entwurf installiert – bitte prüfen und aktivieren'); load() } catch (e) { toast.err(e) }
}
onMounted(() => load().catch(toast.err))
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
    <Tabs v-model="tab" :tabs="[{ id: 'security', label: 'Sicherheit' }, { id: 'skills', label: 'Skills' }, { id: 'tools', label: 'Werkzeuge' }, { id: 'channels', label: 'Kanäle' }]" />
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
