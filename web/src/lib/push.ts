// The browser side of Web Push (docs/ALERTS.md): a service worker
// (public/sw.js) receives pushes from piway through the browser's push
// service and shows them, also when no dashboard tab is open.

/** A subscribed device, as GET /api/v1/push lists it. */
export interface PushDevice {
  id: string
  label: string
  /** The push service's host, such as fcm.googleapis.com. */
  service: string
  created_at: string
  last_ok_at?: string
  last_error?: string
}

export interface PushStatus {
  public_key: string
  subscriptions: PushDevice[]
}

export interface TestResult {
  id: string
  label: string
  ok: boolean
  error?: string
}

async function json<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init)
  if (res.status === 204) return undefined as T
  const body = await res.json().catch(() => null)
  if (!res.ok) throw new Error(body?.error ?? `${path}: HTTP ${res.status}`)
  return body as T
}

export const fetchPushStatus = (signal?: AbortSignal) => json<PushStatus>('/api/v1/push', { signal })

export const removeDevice = (id: string) =>
  json<void>(`/api/v1/push/subscriptions/${encodeURIComponent(id)}`, { method: 'DELETE' })

export const sendTest = (id?: string) =>
  json<{ results: TestResult[] }>('/api/v1/push/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(id ? { id } : {}),
  }).then((r) => r.results)

/** Why this browser cannot subscribe, or null if it can. */
export function unsupportedReason(): string | null {
  if (!window.isSecureContext) return 'insecure'
  if (isIOS() && !isStandalone()) return 'ios-home-screen'
  if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) return 'unsupported'
  return null
}

const isIOS = () => /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
const isStandalone = () =>
  matchMedia('(display-mode: standalone)').matches || (navigator as Navigator & { standalone?: boolean }).standalone === true

/** This browser's subscription, if it has one. */
export async function currentSubscription(): Promise<PushSubscription | null> {
  const reg = await navigator.serviceWorker.getRegistration('/')
  return (await reg?.pushManager.getSubscription()) ?? null
}

/** The id piway gives a subscription: the first 8 bytes of SHA-256(endpoint), hex. */
export async function deviceId(endpoint: string): Promise<string> {
  const sum = new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(endpoint)))
  return [...sum.slice(0, 8)].map((b) => b.toString(16).padStart(2, '0')).join('')
}

/** Asks for permission, subscribes this browser and registers it with piway. */
export async function enablePush(publicKey: string): Promise<PushDevice> {
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') throw new PermissionError(permission)
  const reg = await navigator.serviceWorker.register('/sw.js', { scope: '/' })
  await navigator.serviceWorker.ready
  const key = base64urlToBytes(publicKey)
  let sub = await reg.pushManager.getSubscription()
  // Subscribed with another key (piway's key was replaced): start over.
  if (sub && !sameBytes(sub.options.applicationServerKey, key)) {
    await sub.unsubscribe()
    sub = null
  }
  sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
  return json<PushDevice>('/api/v1/push/subscriptions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ subscription: sub.toJSON(), label: deviceLabel() }),
  })
}

/** Unsubscribes this browser and removes it from piway. */
export async function disablePush(): Promise<void> {
  const sub = await currentSubscription()
  if (!sub) return
  await removeDevice(await deviceId(sub.endpoint)).catch(() => {}) // already gone is fine
  await sub.unsubscribe()
}

export class PermissionError extends Error {
  readonly permission: NotificationPermission
  constructor(permission: NotificationPermission) {
    super(permission === 'denied' ? 'Notifications are blocked for this site.' : 'Notifications were not allowed.')
    this.permission = permission
  }
}

function base64urlToBytes(s: string): Uint8Array<ArrayBuffer> {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4)
  const bin = atob(b64)
  const out = new Uint8Array(new ArrayBuffer(bin.length))
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

function sameBytes(a: ArrayBuffer | null, b: Uint8Array): boolean {
  if (!a || a.byteLength !== b.length) return false
  const x = new Uint8Array(a)
  return x.every((v, i) => v === b[i])
}

/** A readable name for this device, such as "Chrome on Android". */
export function deviceLabel(ua = navigator.userAgent): string {
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /Firefox\//.test(ua)
      ? 'Firefox'
      : /Chrome\//.test(ua)
        ? 'Chrome'
        : /Safari\//.test(ua)
          ? 'Safari'
          : 'Browser'
  const os = /iPhone/.test(ua)
    ? 'iPhone'
    : /iPad/.test(ua)
      ? 'iPad'
      : /Android/.test(ua)
        ? 'Android'
        : /Mac OS X/.test(ua)
          ? 'macOS'
          : /Windows/.test(ua)
            ? 'Windows'
            : /Linux/.test(ua)
              ? 'Linux'
              : ''
  return os ? `${browser} on ${os}` : browser
}
