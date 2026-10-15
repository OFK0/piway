/** GET /api/v1/session (docs/API.md › Sign-in). */
export interface Session {
  /** "password" once a password is set; "local" answers only the Pi itself. */
  mode: 'password' | 'local'
  authenticated: boolean
  expires_at?: string
}

export const fetchSession = async (): Promise<Session> => {
  const res = await fetch('/api/v1/session', { cache: 'no-store' })
  if (!res.ok) throw new Error(`/api/v1/session: HTTP ${res.status}`)
  return res.json() as Promise<Session>
}

/** Why a sign-in failed, with the seconds to wait when locked out. */
export class LoginError extends Error {
  readonly retryAfter?: number
  constructor(message: string, retryAfter?: number) {
    super(message)
    this.retryAfter = retryAfter
  }
}

export async function login(password: string): Promise<Session> {
  const res = await fetch('/api/v1/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password }),
  })
  const body = await res.json().catch(() => null)
  if (!res.ok) {
    const retry = Number(res.headers.get('Retry-After')) || undefined
    throw new LoginError(body?.error ?? `HTTP ${res.status}`, retry)
  }
  return body as Session
}

export async function logout(): Promise<void> {
  await fetch('/api/v1/logout', { method: 'POST' })
}

const signedOutEvent = 'piway:signed-out'

/** Tells the app that the server no longer accepts this browser's session. */
export function notifySignedOut() {
  window.dispatchEvent(new Event(signedOutEvent))
}

export function onSignedOut(f: () => void): () => void {
  window.addEventListener(signedOutEvent, f)
  return () => window.removeEventListener(signedOutEvent, f)
}

/** Answers that mean the browser must sign in (again). */
export const needsSignIn = (status: number) => status === 401 || status === 403

/** An API token as listed; the token itself is only in TokenCreated. */
export interface APIToken {
  id: string
  name: string
  /** The token's first characters, to tell tokens apart. */
  hint: string
  created_at: string
  last_used_at?: string
}

export interface TokenCreated extends APIToken {
  token: string
}

async function tokensJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init)
  if (res.status === 401) notifySignedOut() // 403 here means "use a browser session", not "signed out"
  if (res.status === 204) return undefined as T
  const body = await res.json().catch(() => null)
  if (!res.ok) throw new Error(body?.error ?? `${path}: HTTP ${res.status}`)
  return body as T
}

export const fetchTokens = (signal?: AbortSignal) =>
  tokensJSON<{ tokens: APIToken[] }>('/api/v1/tokens', { signal }).then((r) => r.tokens)

export const createToken = (name: string) =>
  tokensJSON<TokenCreated>('/api/v1/tokens', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name }),
  })

export const revokeToken = (id: string) => tokensJSON<void>(`/api/v1/tokens/${encodeURIComponent(id)}`, { method: 'DELETE' })
