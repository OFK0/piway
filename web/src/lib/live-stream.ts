import type { Snapshot } from '../api.ts'
import { Backoff } from './backoff.ts'
import { RingBuffer } from './ring-buffer.ts'

// The dashboard's connection to GET /api/v1/stream (docs/API.md).
//
// Framework-free on purpose: React reads it through useSyncExternalStore,
// and the reconnect logic is unit-tested with a fake socket and fake timers.

export type Status =
  | 'connecting' // first attempt, nothing received yet
  | 'live' // receiving snapshots
  | 'reconnecting' // lost the connection; a retry is scheduled
  | 'offline' // the browser reports no network; waits for 'online'

export interface LiveState {
  status: Status
  latest: Snapshot | null
  /** Oldest to newest, at most `capacity` snapshots. */
  history: Snapshot[]
  /** Epoch ms of the last message, null before the first. */
  receivedAt: number | null
  /** Epoch ms of the next reconnect attempt while reconnecting. */
  retryAt: number | null
  /** Failed attempts since the last healthy connection. */
  attempts: number
}

/** The subset of WebSocket this class uses, so tests can fake it. */
export interface SocketLike {
  onopen: ((ev: unknown) => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: ((ev: { code: number; reason: string }) => void) | null
  onerror: ((ev: unknown) => void) | null
  close(code?: number, reason?: string): void
}

export interface LiveStreamOptions {
  url: string
  /** Snapshots kept for charts. 300 = five minutes at the default interval. */
  capacity?: number
  createSocket?: (url: string) => SocketLike
  backoff?: Backoff
  now?: () => number
  setTimeout?: (fn: () => void, ms: number) => unknown
  clearTimeout?: (id: unknown) => void
}

// Reconnect delays for a LAN dashboard: start under a second, never wait
// more than 5 s, so a restarted piway is picked up within a few seconds.
// A handful of dashboards cannot overload the Pi at that rate, so a little
// jitter is enough, and the header's countdown stays regular: 1, 1, 2, 4,
// then 5 s per attempt instead of a random 0–5 s each time.
const defaultBackoff = () => new Backoff({ base: 500, cap: 5_000, jitter: 0.2 })

// If no snapshot arrives for this long (or 3 intervals, whichever is
// longer), the connection is presumed dead even if the socket has not
// noticed yet, for example after a laptop wakes from sleep.
const minStaleMs = 5_000

export class LiveStream {
  private state: LiveState
  private readonly listeners = new Set<() => void>()
  private readonly buffer: RingBuffer<Snapshot>
  private readonly opts: Required<Omit<LiveStreamOptions, 'capacity' | 'backoff'>>
  private readonly backoff: Backoff
  private socket: SocketLike | null = null
  private retryTimer: unknown = null
  private staleTimer: unknown = null
  private running = false

  constructor(opts: LiveStreamOptions) {
    this.buffer = new RingBuffer(opts.capacity ?? 300)
    this.backoff = opts.backoff ?? defaultBackoff()
    this.opts = {
      url: opts.url,
      createSocket: opts.createSocket ?? ((url) => new WebSocket(url) as unknown as SocketLike),
      now: opts.now ?? Date.now,
      setTimeout: opts.setTimeout ?? ((fn, ms) => globalThis.setTimeout(fn, ms)),
      clearTimeout: opts.clearTimeout ?? ((id) => globalThis.clearTimeout(id as number)),
    }
    this.state = { status: 'connecting', latest: null, history: [], receivedAt: null, retryAt: null, attempts: 0 }
  }

  // --- store interface for useSyncExternalStore -------------------------

  getState = (): LiveState => this.state

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  // --- lifecycle --------------------------------------------------------

  start(): void {
    if (this.running) return
    this.running = true
    this.connect()
  }

  stop(): void {
    this.running = false
    this.clearTimers()
    const s = this.socket
    this.socket = null
    s?.close(1000, 'dashboard closed')
  }

  /** Reconnect now instead of waiting for the scheduled retry. */
  retryNow(): void {
    if (!this.running || this.socket) return
    this.clearTimers()
    this.connect()
  }

  /** Browser network events: offline pauses retries, online resumes at once. */
  setOnline(online: boolean): void {
    if (!this.running) return
    if (!online) {
      this.clearTimers()
      this.socket?.close(1000, 'offline')
      this.socket = null
      this.set({ status: 'offline', retryAt: null })
      return
    }
    if (this.state.status === 'offline') {
      this.backoff.reset()
      this.connect()
    }
  }

  // --- internals --------------------------------------------------------

  private connect(): void {
    this.set({ status: this.state.attempts > 0 || this.state.latest ? 'reconnecting' : 'connecting', retryAt: null })
    let socket: SocketLike
    try {
      socket = this.opts.createSocket(this.opts.url)
    } catch {
      this.scheduleRetry()
      return
    }
    this.socket = socket

    socket.onmessage = (ev) => {
      if (this.socket !== socket) return
      let snap: Snapshot
      try {
        snap = JSON.parse(String(ev.data)) as Snapshot
      } catch {
        return // not a snapshot; ignore rather than drop the connection
      }
      this.buffer.push(snap)
      // A message proves the connection healthy: the next outage starts
      // its backoff from the beginning again.
      this.backoff.reset()
      this.set({
        status: 'live',
        latest: snap,
        history: this.buffer.toArray(),
        receivedAt: this.opts.now(),
        attempts: 0,
        retryAt: null,
      })
      this.armStaleTimer()
    }

    socket.onclose = () => {
      if (this.socket !== socket) return // replaced or stopped on purpose
      this.socket = null
      if (this.running && this.state.status !== 'offline') this.scheduleRetry()
    }

    // Errors are always followed by close, which does the work.
    socket.onerror = () => {}
  }

  private scheduleRetry(): void {
    this.clearTimers()
    const delay = this.backoff.next()
    this.set({ status: 'reconnecting', retryAt: this.opts.now() + delay, attempts: this.backoff.attempts })
    this.retryTimer = this.opts.setTimeout(() => {
      this.retryTimer = null
      if (this.running) this.connect()
    }, delay)
  }

  private armStaleTimer(): void {
    if (this.staleTimer !== null) this.opts.clearTimeout(this.staleTimer)
    this.staleTimer = this.opts.setTimeout(() => {
      this.staleTimer = null
      // Close our side; onclose would not fire for a half-open socket,
      // so drop it and schedule the retry ourselves.
      const s = this.socket
      this.socket = null
      s?.close(4000, 'no data')
      if (this.running) this.scheduleRetry()
    }, this.staleAfter())
  }

  /** 3 × the observed interval between the last two snapshots, at least 5 s. */
  private staleAfter(): number {
    const h = this.state.history
    if (h.length < 2) return minStaleMs * 2
    const gap = Date.parse(h[h.length - 1].time) - Date.parse(h[h.length - 2].time)
    return Math.max(minStaleMs, Number.isFinite(gap) ? gap * 3 : 0)
  }

  private clearTimers(): void {
    if (this.retryTimer !== null) this.opts.clearTimeout(this.retryTimer)
    if (this.staleTimer !== null) this.opts.clearTimeout(this.staleTimer)
    this.retryTimer = null
    this.staleTimer = null
  }

  private set(patch: Partial<LiveState>): void {
    this.state = { ...this.state, ...patch }
    for (const l of this.listeners) l()
  }
}

/** The stream URL for the page's own origin (ws: or wss: to match). */
export function streamURL(loc: { protocol: string; host: string } = window.location): string {
  return `${loc.protocol === 'https:' ? 'wss' : 'ws'}://${loc.host}/api/v1/stream`
}
