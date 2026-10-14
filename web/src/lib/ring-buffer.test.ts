import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { RingBuffer } from './ring-buffer.ts'

describe('RingBuffer', () => {
  it('keeps items in insertion order until full', () => {
    const r = new RingBuffer<number>(3)
    assert.deepEqual(r.toArray(), [])
    assert.equal(r.last(), undefined)
    r.push(1)
    r.push(2)
    assert.deepEqual(r.toArray(), [1, 2])
    assert.equal(r.size, 2)
    assert.equal(r.last(), 2)
  })

  it('overwrites the oldest item once full', () => {
    const r = new RingBuffer<number>(3)
    for (let i = 1; i <= 7; i++) r.push(i)
    assert.deepEqual(r.toArray(), [5, 6, 7])
    assert.equal(r.size, 3)
    assert.equal(r.last(), 7)
  })

  it('wraps correctly across many cycles', () => {
    const r = new RingBuffer<number>(4)
    for (let i = 0; i < 1003; i++) r.push(i)
    assert.deepEqual(r.toArray(), [999, 1000, 1001, 1002])
  })

  it('returns a copy that later pushes do not change', () => {
    const r = new RingBuffer<number>(2)
    r.push(1)
    const snapshot = r.toArray()
    r.push(2)
    r.push(3)
    assert.deepEqual(snapshot, [1])
  })

  it('clears to empty and can be reused', () => {
    const r = new RingBuffer<string>(2)
    r.push('a')
    r.push('b')
    r.push('c')
    r.clear()
    assert.equal(r.size, 0)
    assert.deepEqual(r.toArray(), [])
    r.push('d')
    assert.deepEqual(r.toArray(), ['d'])
  })

  it('works with capacity 1', () => {
    const r = new RingBuffer<number>(1)
    r.push(1)
    r.push(2)
    assert.deepEqual(r.toArray(), [2])
  })

  it('rejects a capacity that is not a positive integer', () => {
    for (const c of [0, -1, 1.5, Number.NaN]) {
      assert.throws(() => new RingBuffer(c), RangeError)
    }
  })
})
