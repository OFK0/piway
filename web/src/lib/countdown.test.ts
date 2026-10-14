import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { msUntilNextSecond, secondsLeft } from './countdown.ts'

describe('countdown', () => {
  it('rounds the seconds left up and never goes below zero', () => {
    assert.equal(secondsLeft(10_000, 7_700), 3)
    assert.equal(secondsLeft(10_000, 8_000), 2)
    assert.equal(secondsLeft(10_000, 9_999), 1)
    assert.equal(secondsLeft(10_000, 10_000), 0)
    assert.equal(secondsLeft(10_000, 12_000), 0)
  })

  it('wakes just after the displayed second changes', () => {
    assert.equal(msUntilNextSecond(10_000, 7_700), 301) // 3 s -> 2 s at 8001
    assert.equal(msUntilNextSecond(10_000, 8_000), 1001) // 2 s for a full second
    assert.equal(msUntilNextSecond(10_000, 10_000), null)
  })

  it('shows every second once, each for one second', () => {
    // Follow the wake-ups from 2.3 s before the deadline.
    const deadline = 10_000
    let now = 7_700
    const shown: [number, number][] = []
    for (let wait = msUntilNextSecond(deadline, now); wait !== null; wait = msUntilNextSecond(deadline, now)) {
      shown.push([secondsLeft(deadline, now), wait])
      now += wait
    }
    assert.deepEqual(
      shown.map(([s]) => s),
      [3, 2, 1],
    )
    assert.equal(secondsLeft(deadline, now), 0)
  })
})
