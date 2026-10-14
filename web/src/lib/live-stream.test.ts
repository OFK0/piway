import assert from 'node:assert/strict'
import { beforeEach, describe, it } from 'node:test'

import { Backoff } from './backoff.ts'
import { LiveStream, streamURL, type SocketLike } from './live-stream.ts'

class FakeSocket implements SocketLike {
  onopen: SocketLike['onopen'] = null
  onmessage: SocketLike['onmessage'] = null
  onclose: SocketLike['onclose'] = null
  onerror: SocketLike['onerror'] = null
  closed: { code?: number; reason?: string } | null = null
  readonly url: string
  constructor(url: string) {
    this.url = url
  }
  close(code?: number, reason?: string) {
    this.closed = { code, reason }
  }
  // Server side helpers.
  send(seq: number, timeMs = seq * 1000) {
    this.onmessage?.({ data: JSON.stringify({ seq, time: new Date(timeMs).toISOString() }) })
  }
  drop(code = 1006) {
    this.onclose?.({ code, reason: '' })
  }
}

// A manual clock: timers fire only when the test advances time.
class Clock {
  now = 0
  private timers = new Map<number, { at: number; fn: () => void }>()
  private nextId = 1
  setTimeout = (fn: () => void, ms: number) => {
    const id = this.nextId++
    this.timers.set(id, { at: this.now + ms, fn })
    return id
  }
  clearTimeout = (id: unknown) => {
    this.timers.delete(id as number)
  }
  advance(ms: number) {
    const until = this.now + ms
    for (;;) {
      const due = [...this.timers].filter(([, t]) => t.at <= until).sort((a, b) => a[1].at - b[1].at)[0]
      if (!due) break
      this.timers.delete(due[0])
      this.now = due[1].at
      due[1].fn()
    }
    this.now = until
  }
  get pending() {
    return this.timers.size
  }
}

let clock: Clock
let sockets: FakeSocket[]
let stream: LiveStream

function setup(capacity = 300) {
  clock = new Clock()
  sockets = []
  stream = new LiveStream({
    url: 'ws://pi/api/v1/stream',
    capacity,
    createSocket: (url) => {
      const s = new FakeSocket(url)
      sockets.push(s)
      return s
    },
    // Deterministic jitter: always half the ceiling.
    backoff: new Backoff({ base: 500, cap: 10_000, random: () => 0.5 }),
    now: () => clock.now,
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
  })
}

const last = () => sockets[sockets.length - 1]

describe('LiveStream', () => {
  beforeEach(() => setup())

  it('connects on start and goes live on the first snapshot', () => {
    assert.equal(stream.getState().status, 'connecting')
    stream.start()
    assert.equal(sockets.length, 1)
    assert.equal(last().url, 'ws://pi/api/v1/stream')

    last().send(1)
    const s = stream.getState()
    assert.equal(s.status, 'live')
    assert.equal(s.latest?.seq, 1)
    assert.deepEqual(s.history.map((x) => x.seq), [1])
  })

  it('notifies subscribers on every change and stops after unsubscribe', () => {
    let calls = 0
    const unsubscribe = stream.subscribe(() => calls++)
    stream.start()
    last().send(1)
    const before = calls
    assert.ok(before >= 2)
    unsubscribe()
    last().send(2)
    assert.equal(calls, before)
  })

  it('reconnects with growing, jittered delays and resets after a healthy message', () => {
    stream.start()
    last().send(1)

    // piway goes away: 250, 500, 1000 ms (half of 500, 1000, 2000).
    const waits: number[] = []
    for (let i = 0; i < 3; i++) {
      last().drop(1001)
      const st = stream.getState()
      assert.equal(st.status, 'reconnecting')
      waits.push(st.retryAt! - clock.now)
      const before = sockets.length
      clock.advance(st.retryAt! - clock.now)
      assert.equal(sockets.length, before + 1, 'a new socket after the delay')
    }
    assert.deepEqual(waits, [250, 500, 1000])
    assert.equal(stream.getState().attempts, 3)

    // It is back: the next outage starts small again.
    last().send(2)
    assert.equal(stream.getState().status, 'live')
    assert.equal(stream.getState().attempts, 0)
    last().drop()
    assert.equal(stream.getState().retryAt! - clock.now, 250)
  })

  it('keeps the data on screen while reconnecting', () => {
    stream.start()
    last().send(1)
    last().send(2)
    last().drop()
    const s = stream.getState()
    assert.equal(s.status, 'reconnecting')
    assert.equal(s.latest?.seq, 2)
    assert.equal(s.history.length, 2)
  })

  it('caps the history at its capacity', () => {
    setup(3)
    stream.start()
    for (let i = 1; i <= 10; i++) last().send(i)
    assert.deepEqual(stream.getState().history.map((x) => x.seq), [8, 9, 10])
  })

  it('treats a silent connection as dead', () => {
    stream.start()
    last().send(1, 1000)
    last().send(2, 2000) // 1 s interval → stale after max(5 s, 3 s) = 5 s
    const first = last()
    clock.advance(4_999)
    assert.equal(stream.getState().status, 'live')
    clock.advance(1)
    assert.equal(first.closed?.code, 4000)
    assert.equal(stream.getState().status, 'reconnecting')
    clock.advance(250)
    assert.notEqual(last(), first, 'reconnected with a new socket')
  })

  it('waits for the network instead of retrying while offline', () => {
    stream.start()
    last().send(1)
    stream.setOnline(false)
    assert.equal(stream.getState().status, 'offline')
    assert.equal(clock.pending, 0, 'no retry timers while offline')

    const count = sockets.length
    clock.advance(60_000)
    assert.equal(sockets.length, count)

    stream.setOnline(true)
    assert.equal(sockets.length, count + 1, 'reconnects at once when back online')
  })

  it('retryNow skips the wait', () => {
    stream.start()
    last().drop()
    assert.equal(stream.getState().status, 'reconnecting')
    stream.retryNow()
    assert.equal(sockets.length, 2)
    assert.equal(clock.pending, 0)
  })

  it('stop closes the socket and never reconnects', () => {
    stream.start()
    last().send(1)
    const s = last()
    stream.stop()
    assert.equal(s.closed?.code, 1000)
    s.drop() // the late close event of the stopped socket
    clock.advance(60_000)
    assert.equal(sockets.length, 1)
  })

  it('ignores messages that are not JSON', () => {
    stream.start()
    last().onmessage?.({ data: 'not json' })
    assert.equal(stream.getState().status, 'connecting')
    assert.equal(stream.getState().latest, null)
  })
})

describe('streamURL', () => {
  it('matches the page scheme', () => {
    assert.equal(streamURL({ protocol: 'http:', host: 'pi:8080' }), 'ws://pi:8080/api/v1/stream')
    assert.equal(streamURL({ protocol: 'https:', host: 'pi.example' }), 'wss://pi.example/api/v1/stream')
  })
})
