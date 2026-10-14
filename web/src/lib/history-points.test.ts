import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import type { SeriesResponse } from '../api.ts'
import { historyPoints } from './history-points.ts'

const res = (series: Record<string, [number, number][]>): SeriesResponse => ({
  resolution: '1m',
  step_seconds: 300,
  from: '',
  to: '',
  series: Object.entries(series).map(([name, pts]) => ({
    name,
    points: pts.map(([t, v]) => [t, v, v, v] as [number, number, number, number]),
  })),
})

describe('historyPoints', () => {
  it('merges series by time, oldest first', () => {
    const p = historyPoints({
      cpu: res({ cpu_usage_percent: [[2000, 20], [1000, 10]] }),
      temp: res({ temperature_celsius: [[1000, 45]] }),
    })
    assert.deepEqual(
      p.map((x) => [x.t, x.cpu, x.temp]),
      [
        [1000, 10, 45],
        [2000, 20, null],
      ],
    )
  })

  it('sums network over interfaces', () => {
    const p = historyPoints({
      rx: res({
        'network_receive_bytes_per_second{interface="eth0"}': [[1000, 100]],
        'network_receive_bytes_per_second{interface="wlan0"}': [[1000, 50]],
      }),
    })
    assert.equal(p[0].rx, 150)
  })

  it('computes memory used percent and leaves gaps without both parts', () => {
    const p = historyPoints({
      memUsed: res({ memory_used_bytes: [[1000, 1], [2000, 3]] }),
      memTotal: res({ memory_total_bytes: [[1000, 4]] }),
    })
    assert.equal(p[0].mem, 25)
    assert.equal(p[1].mem, null)
  })

  it('handles no data', () => {
    assert.deepEqual(historyPoints({}), [])
  })
})
