// API-Client: JSON, CSRF-Header, Problem Details (RFC 9457) und Step-up-Handling.
export class ApiError extends Error {
  constructor(public status: number, message: string, public stepUp = false, public body?: any) {
    super(message)
  }
}

type StepUpHandler = () => Promise<boolean>
let stepUpHandler: StepUpHandler | null = null
export function onStepUp(h: StepUpHandler) {
  stepUpHandler = h
}
let unauthorized: (() => void) | null = null
export function onUnauthorized(h: () => void) {
  unauthorized = h
}

export async function api<T = any>(method: string, path: string, body?: unknown, retry = true): Promise<T> {
  const res = await fetch('/api/v1' + path, {
    method,
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      'X-Requested-With': 'fylgja',
      ...(method !== 'GET' ? { 'Idempotency-Key': crypto.randomUUID() } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let data: any = undefined
  try {
    data = text ? JSON.parse(text) : undefined
  } catch {
    data = text
  }
  if (res.ok) return data as T
  const needStepUp = res.status === 428 || res.headers.get('X-Fylgja-Step-Up') === 'required'
  if (needStepUp && retry && stepUpHandler) {
    if (await stepUpHandler()) return api<T>(method, path, body, false)
  }
  if (res.status === 401 && unauthorized && !path.startsWith('/auth/')) unauthorized()
  throw new ApiError(res.status, data?.detail || data?.error || res.statusText, needStepUp, data)
}

export const get = <T = any>(p: string) => api<T>('GET', p)
export const post = <T = any>(p: string, b?: unknown) => api<T>('POST', p, b ?? {})
export const patch = <T = any>(p: string, b: unknown) => api<T>('PATCH', p, b)
export const put = <T = any>(p: string, b: unknown) => api<T>('PUT', p, b)
export const del = <T = any>(p: string) => api<T>('DELETE', p)

// ---- WebAuthn-Hilfen (base64url ↔ ArrayBuffer) ----
const b64u = {
  dec(s: string): ArrayBuffer {
    const pad = '='.repeat((4 - (s.length % 4)) % 4)
    const bin = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'))
    const buf = new Uint8Array(bin.length)
    for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i)
    return buf.buffer
  },
  enc(b: ArrayBuffer): string {
    let s = ''
    new Uint8Array(b).forEach((x) => (s += String.fromCharCode(x)))
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  },
}

function prepCreate(o: any): CredentialCreationOptions {
  const pk = o.publicKey
  pk.challenge = b64u.dec(pk.challenge)
  pk.user.id = b64u.dec(pk.user.id)
  pk.excludeCredentials = (pk.excludeCredentials || []).map((c: any) => ({ ...c, id: b64u.dec(c.id) }))
  return o
}
function prepGet(o: any): CredentialRequestOptions {
  const pk = o.publicKey
  pk.challenge = b64u.dec(pk.challenge)
  pk.allowCredentials = (pk.allowCredentials || []).map((c: any) => ({ ...c, id: b64u.dec(c.id) }))
  return o
}
function credJSON(c: any) {
  const r = c.response
  const out: any = { id: c.id, rawId: b64u.enc(c.rawId), type: c.type, response: { clientDataJSON: b64u.enc(r.clientDataJSON) } }
  if (r.attestationObject) out.response.attestationObject = b64u.enc(r.attestationObject)
  if (r.authenticatorData) out.response.authenticatorData = b64u.enc(r.authenticatorData)
  if (r.signature) out.response.signature = b64u.enc(r.signature)
  if (r.userHandle) out.response.userHandle = b64u.enc(r.userHandle)
  return out
}

async function raw(path: string, body: unknown) {
  const res = await fetch('/api/v1' + path, {
    method: 'POST', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'fylgja' }, body: JSON.stringify(body),
  })
  if (!res.ok) throw new ApiError(res.status, (await res.json().catch(() => ({})))?.detail || 'fehlgeschlagen')
  return res.json()
}

export const passkeys = {
  supported: () => typeof window !== 'undefined' && !!window.PublicKeyCredential,
  async register() {
    const { options, key } = await post('/auth/passkey/register/begin')
    const cred = await navigator.credentials.create(prepCreate(options))
    return raw('/auth/passkey/register/finish?key=' + encodeURIComponent(key), credJSON(cred))
  },
  async login() {
    const { options, key } = await post('/auth/passkey/login/begin')
    const cred = await navigator.credentials.get(prepGet(options))
    return raw('/auth/passkey/login/finish?key=' + encodeURIComponent(key), credJSON(cred))
  },
  async stepUp() {
    const { options, key } = await post('/auth/passkey/stepup/begin')
    const cred = await navigator.credentials.get(prepGet(options))
    return raw('/auth/passkey/stepup/finish?key=' + encodeURIComponent(key), credJSON(cred))
  },
}
