import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { ago, duration, shortDuration, shortSha } from './time.ts'

describe('duration', () => {
  it('formats seconds and minutes', () => {
    const t0 = '2026-10-14T12:00:00Z'
    assert.equal(duration(t0, '2026-10-14T12:00:45Z'), '45 s')
    assert.equal(duration(t0, '2026-10-14T12:01:12Z'), '1 m 12 s')
    assert.equal(duration(t0, '2026-10-14T12:03:00Z'), '3 m')
    assert.equal(duration(t0, '2026-10-14T11:59:00Z'), '0 s')
  })
  it('shortens commits', () => {
    assert.equal(shortSha('b691d31695bad228'), 'b691d31')
  })
})

describe('ago', () => {
  const now = Date.parse('2026-10-14T12:00:00Z')
  it('never says "in N seconds" for something that already happened', () => {
    const zero = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' }).format(0, 'second')
    assert.equal(ago('2026-10-14T12:00:15Z', now), zero) // now lags the data
    assert.equal(ago('2026-10-14T11:59:57Z', now), zero)
  })
  it('counts back otherwise', () => {
    assert.match(ago('2026-10-14T11:57:00Z', now), /3/)
    assert.match(ago('2026-10-14T11:57:00Z', now), /ago|önce/)
  })
})

describe('shortDuration', () => {
  it('drops zero parts', () => {
    for (const [d, want] of [['5m0s', '5m'], ['1h0m0s', '1h'], ['1h30m0s', '1h30m'], ['30s', '30s'], ['1m30s', '1m30s'], ['0s', '0s'], ['10s', '10s']]) {
      assert.equal(shortDuration(d), want, d)
    }
  })
})
