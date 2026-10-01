// Live-Gespräch: Mikrofon mit Sprechpausen-Erkennung, Aufnahme → /voice/transcribe, Antwort satzweise über
// /voice/speak abspielen. Wer währenddessen spricht, unterbricht die Ausgabe (Barge-in).

export type VoiceState = 'idle' | 'listening' | 'hearing' | 'transcribing' | 'thinking' | 'speaking'

export interface VoiceOpts {
  dotId: string
  speak: boolean // TTS verfügbar
  onState: (s: VoiceState) => void
  onText: (text: string) => void
  onBarge: () => void // Nutzer unterbricht die Ausgabe
  onError: (msg: string) => void
}

const SILENCE_MS = 1100
const MIN_SPEECH_MS = 450
const START_MS = 100
const BARGE_MS = 350

function recorderType(): string {
  for (const t of ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus']) if (MediaRecorder.isTypeSupported(t)) return t
  return ''
}

// Markdown für die Sprachausgabe säubern; Code-Blöcke werden nicht vorgelesen.
export function speakable(md: string): string {
  return md
    .replace(/```[\s\S]*?(```|$)/g, ' Codeblock ausgelassen. ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/^#{1,6}\s*/gm, '')
    .replace(/[*_~>]+/g, '')
    .replace(/^\s*[-+]\s+/gm, '')
    .replace(/https?:\/\/\S+/g, ' Link ')
    .replace(/\s+/g, ' ')
    .trim()
}

// Zerlegt Text in fertige Sätze; der unfertige Rest bleibt zurück.
export function splitSentences(text: string, final: boolean): { done: string[]; rest: string } {
  const done: string[] = []
  let rest = text
  const re = /^(.{12,}?[.!?…:])(\s+|$)/s
  for (;;) {
    const m = re.exec(rest)
    if (!m || (!final && m[2] === '' && m[0].length === rest.length)) break
    done.push(m[1].trim())
    rest = rest.slice(m[0].length)
  }
  if (final && rest.trim()) { done.push(rest.trim()); rest = '' }
  return { done, rest }
}

export class VoiceSession {
  private o: VoiceOpts
  private stream?: MediaStream
  private ctx?: AudioContext
  private an?: AnalyserNode
  private buf = new Float32Array(1024)
  private timer?: number
  private rec?: MediaRecorder
  private chunks: Blob[] = []
  private floor = 0.01
  private calib: number[] = []
  private loudSince = 0
  private quietSince = 0
  private speechStart = 0
  private bargeSince = 0
  private state: VoiceState = 'idle'
  private gen = 0
  private audio = new Audio()
  private queue: Promise<string | null>[] = []
  private playing = false
  private spoken = 0 // bereits an die Warteschlange gegebene Zeichen von speakable(text)
  private pendingRest = ''
  private agentBusy = false

  constructor(o: VoiceOpts) { this.o = o }

  private set(s: VoiceState) { if (this.state !== s) { this.state = s; this.o.onState(s) } }

  async start() {
    this.stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } })
    this.ctx = new AudioContext()
    const src = this.ctx.createMediaStreamSource(this.stream)
    this.an = this.ctx.createAnalyser()
    this.an.fftSize = 1024
    src.connect(this.an)
    this.set('listening')
    this.timer = window.setInterval(() => this.tick(), 50)
  }

  stop() {
    clearInterval(this.timer)
    this.cancelSpeech()
    try { if (this.rec?.state === 'recording') { this.rec.onstop = null; this.rec.stop() } } catch { /* */ }
    this.stream?.getTracks().forEach((t) => t.stop())
    this.ctx?.close().catch(() => {})
    this.set('idle')
  }

  private rms(): number {
    this.an!.getFloatTimeDomainData(this.buf)
    let s = 0
    for (const v of this.buf) s += v * v
    return Math.sqrt(s / this.buf.length)
  }

  private tick() {
    if (!this.an) return
    const now = performance.now()
    const level = this.rms()
    if (this.calib.length < 12) { this.calib.push(level); if (this.calib.length === 12) this.floor = Math.min(0.05, this.calib.reduce((a, b) => a + b, 0) / 12); return }
    const thr = Math.max(0.02, this.floor * 3)
    const speaking = this.state === 'speaking'
    if (this.state === 'hearing') {
      if (level < thr * 0.6) {
        if (!this.quietSince) this.quietSince = now
        if (now - this.quietSince > SILENCE_MS) this.finishUtterance(now)
      } else this.quietSince = 0
      return
    }
    if (this.state === 'transcribing') return
    // listening | thinking | speaking
    const need = speaking ? thr * 1.5 : thr
    if (level > need) {
      if (!this.loudSince) this.loudSince = now
      if (now - this.loudSince > (speaking ? BARGE_MS : START_MS)) {
        this.loudSince = 0
        if (speaking) { this.cancelSpeech(); this.o.onBarge() }
        this.beginUtterance(now)
      }
    } else this.loudSince = 0
  }

  private beginUtterance(now: number) {
    const type = recorderType()
    this.chunks = []
    this.rec = new MediaRecorder(this.stream!, type ? { mimeType: type } : undefined)
    this.rec.ondataavailable = (e) => { if (e.data.size) this.chunks.push(e.data) }
    this.rec.start(200)
    this.speechStart = now
    this.quietSince = 0
    this.set('hearing')
  }

  private finishUtterance(now: number) {
    const dur = now - this.speechStart - SILENCE_MS
    const rec = this.rec!
    const type = rec.mimeType || 'audio/webm'
    rec.onstop = async () => {
      const blob = new Blob(this.chunks, { type })
      this.chunks = []
      if (dur < MIN_SPEECH_MS || blob.size < 2500) { this.set(this.agentBusy ? 'thinking' : 'listening'); return }
      this.set('transcribing')
      try {
        const r = await fetch(`/api/v1/dots/${this.o.dotId}/voice/transcribe`, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': type, 'X-Requested-With': 'fylgja', 'Idempotency-Key': crypto.randomUUID() }, body: blob })
        if (!r.ok) throw new Error((await r.json().catch(() => ({}))).detail || 'Spracherkennung fehlgeschlagen')
        const { text } = await r.json()
        if (text?.trim()) { this.agentBusy = true; this.set('thinking'); this.o.onText(text.trim()) } else this.set(this.agentBusy ? 'thinking' : 'listening')
      } catch (e: any) { this.o.onError(e?.message || 'Spracherkennung fehlgeschlagen'); this.set('listening') }
    }
    rec.stop()
  }

  // ---- Ausgabe ----

  /** Neuen Lauf beginnen: Zähler zurücksetzen. */
  beginAnswer() { this.cancelSpeech(false); this.spoken = 0; this.pendingRest = ''; this.agentBusy = true; if (this.state === 'listening') this.set('thinking') }

  /** Gesamten bisherigen Antworttext übergeben (wächst beim Streamen). */
  feed(fullText: string, final = false) {
    if (!this.o.speak) { if (final) this.endAnswer(); return }
    const plain = speakable(fullText)
    const { done, rest } = splitSentences(plain.slice(this.spoken), final)
    this.spoken = plain.length - rest.length
    for (const s of done) this.enqueue(s)
    if (final) this.endAnswer()
  }

  private endAnswer() { this.agentBusy = false; if (!this.playing && this.queue.length === 0 && this.state === 'thinking') this.set('listening') }

  private enqueue(sentence: string) {
    const gen = this.gen
    const p = fetch(`/api/v1/dots/${this.o.dotId}/voice/speak`, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'fylgja', 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ text: sentence }) })
      .then(async (r) => (r.ok && gen === this.gen ? URL.createObjectURL(await r.blob()) : null))
      .catch(() => null)
    this.queue.push(p)
    if (!this.playing) void this.pump()
  }

  private async pump() {
    this.playing = true
    const gen = this.gen
    while (this.queue.length && gen === this.gen) {
      const url = await this.queue.shift()!
      if (!url || gen !== this.gen) { if (url) URL.revokeObjectURL(url); continue }
      if (this.state !== 'hearing' && this.state !== 'transcribing') this.set('speaking')
      await new Promise<void>((resolve) => {
        this.audio.src = url
        this.audio.onended = this.audio.onerror = () => resolve()
        this.audio.play().catch(() => resolve())
      })
      URL.revokeObjectURL(url)
    }
    this.playing = false
    if (gen === this.gen && this.state === 'speaking') this.set(this.agentBusy ? 'thinking' : 'listening')
  }

  cancelSpeech(resetState = true) {
    this.gen++
    this.queue = []
    this.audio.pause()
    this.playing = false
    if (resetState && this.state === 'speaking') this.set('listening')
  }
}

export async function voiceStatus(): Promise<{ stt: boolean; tts: boolean }> {
  try {
    const r = await fetch('/api/v1/voice/status', { credentials: 'same-origin' })
    return r.ok ? await r.json() : { stt: false, tts: false }
  } catch { return { stt: false, tts: false } }
}
