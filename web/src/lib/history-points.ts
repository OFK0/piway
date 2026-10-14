import type { SeriesResponse } from '../api.ts'

// One time step of the overview charts, from live snapshots or history.
export interface Point {
  t: number
  cpu: number | null
  mem: number | null
  temp: number | null
  rx: number | null
  tx: number | null
}

/** The queries the 24-hour charts need, keyed by what they feed. */
export const historyQueries = {
  cpu: 'cpu_usage_percent',
  temp: 'temperature_celsius',
  memUsed: 'memory_used_bytes',
  memTotal: 'memory_total_bytes',
  rx: 'network_receive_bytes_per_second',
  tx: 'network_transmit_bytes_per_second',
} as const

export type HistoryKey = keyof typeof historyQueries

/** time -> value: one series' averages, or the sum over all its label sets. */
function byTime(res: SeriesResponse | undefined): Map<number, number> {
  const out = new Map<number, number>()
  for (const s of res?.series ?? []) {
    for (const [t, avg] of s.points) out.set(t, (out.get(t) ?? 0) + avg)
  }
  return out
}

/**
 * Merges the query results into chart points, oldest first. Network is
 * summed over interfaces, memory is used/total in percent. A metric with no
 * value at a time is null there, so the chart shows a gap.
 */
export function historyPoints(results: Partial<Record<HistoryKey, SeriesResponse>>): Point[] {
  const m = Object.fromEntries(
    (Object.keys(historyQueries) as HistoryKey[]).map((k) => [k, byTime(results[k])]),
  ) as Record<HistoryKey, Map<number, number>>

  const times = new Set<number>()
  for (const map of Object.values(m)) for (const t of map.keys()) times.add(t)

  return [...times]
    .sort((a, b) => a - b)
    .map((t) => {
      const used = m.memUsed.get(t)
      const total = m.memTotal.get(t)
      return {
        t,
        cpu: m.cpu.get(t) ?? null,
        temp: m.temp.get(t) ?? null,
        mem: used !== undefined && total ? (used / total) * 100 : null,
        rx: m.rx.get(t) ?? null,
        tx: m.tx.get(t) ?? null,
      }
    })
}
