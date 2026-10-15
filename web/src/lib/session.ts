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
