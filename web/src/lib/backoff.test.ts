import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { Backoff } from './backoff.ts'

describe('Backoff', () => {
  it('doubles the ceiling per attempt up to the cap', () => {
    const b = new Backoff({ base: 500, cap: 10_000, random: () => 0 })
    const ceilings = []
    for (let i = 0; i < 8; i++) {
      ceilings.push(b.ceiling())
      b.next()
    }
    assert.deepEqual(ceilings, [500, 1000, 2000, 4000, 8000, 10_000, 10_000, 10_000])
  })

  it('draws each delay uniformly below the ceiling', () => {
    const draws = [0, 0.5, 0.999999]
    const b = new Backoff({ base: 1000, cap: 1000, random: () => draws.shift()! })
    assert.equal(b.next(), 0)
    assert.equal(b.next(), 500)
    assert.equal(b.next(), 999) // never reaches the ceiling itself
  })

  it('stays within [0, cap) with the real random source', () => {
    const b = new Backoff({ base: 100, cap: 3000 })
    for (let i = 0; i < 1000; i++) {
      const ceiling = b.ceiling()
      const d = b.next()
      assert.ok(d >= 0 && d < ceiling, `delay ${d} outside [0, ${ceiling})`)
    }
  })

  it('spreads clients out instead of synchronising them', () => {
    // 100 dashboards reconnecting after the same restart, attempt 3.
    const delays = Array.from({ length: 100 }, () => {
      const b = new Backoff({ base: 500, cap: 10_000 })
      b.next()
      b.next()
      b.next()
      return b.next()
    })
    const distinct = new Set(delays.map((d) => Math.floor(d / 100))).size
    assert.ok(distinct > 30, `only ${distinct} distinct 100ms buckets`)
  })

  it('does not overflow after very many attempts', () => {
    const b = new Backoff({ base: 500, cap: 10_000, random: () => 0.5 })
    for (let i = 0; i < 2000; i++) b.next()
    assert.equal(b.ceiling(), 10_000)
    assert.equal(b.next(), 5000)
  })

  it('starts over after reset', () => {
    const b = new Backoff({ base: 500, cap: 10_000, random: () => 0 })
    b.next()
    b.next()
    assert.equal(b.attempts, 2)
    b.reset()
    assert.equal(b.attempts, 0)
    assert.equal(b.ceiling(), 500)
  })

  it('keeps delays within the jittered share of the ceiling', () => {
    const draws = [0, 0.5, 0.999999]
    const b = new Backoff({ base: 1000, cap: 1000, jitter: 0.2, random: () => draws.shift()! })
    assert.equal(b.next(), 800)
    assert.equal(b.next(), 900)
    assert.equal(b.next(), 999)
  })

  it('grows steadily with small jitter', () => {
    // The countdown a user sees after each failed attempt, in whole seconds.
    const b = new Backoff({ base: 500, cap: 5_000, jitter: 0.2, random: () => 0.5 })
    const seconds = Array.from({ length: 8 }, () => Math.ceil(b.next() / 1000))
    assert.deepEqual(seconds, [1, 1, 2, 4, 5, 5, 5, 5])
  })

  it('rejects impossible options', () => {
    assert.throws(() => new Backoff({ base: 0, cap: 100 }), RangeError)
    assert.throws(() => new Backoff({ base: 200, cap: 100 }), RangeError)
    assert.throws(() => new Backoff({ base: 100, cap: 100, jitter: 0 }), RangeError)
    assert.throws(() => new Backoff({ base: 100, cap: 100, jitter: 1.5 }), RangeError)
  })
})
