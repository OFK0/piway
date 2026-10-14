import { useEffect, useState } from 'react'

import { fetchSeries, HistoryDisabledError, type SeriesResponse } from '@/api'
import { historyPoints, historyQueries, type HistoryKey, type Point } from '@/lib/history-points'

export interface HistoryState {
  points: Point[]
  loading: boolean
  /** Why history is unavailable, if it is. */
  error: string | null
  /** When the points were fetched (ms): the end of the 24-hour window. */
  at: number
}

// 24 hours in 5-minute buckets: 288 points per chart, served from the 1m tier.
const params = { from: '-24h', step: '5m' }
const refreshMs = 60_000

/** The last 24 hours of the overview metrics, refreshed every minute while active. */
export function useHistory(active: boolean): HistoryState {
  const [state, setState] = useState<HistoryState>({ points: [], loading: true, error: null, at: 0 })

  useEffect(() => {
    if (!active) return
    let ctrl = new AbortController()
    const load = async () => {
      ctrl = new AbortController()
      const keys = Object.keys(historyQueries) as HistoryKey[]
      try {
        const results = await Promise.all(keys.map((k) => fetchSeries(historyQueries[k], params, ctrl.signal)))
        const byKey = Object.fromEntries(keys.map((k, i) => [k, results[i]])) as Record<HistoryKey, SeriesResponse>
        setState({ points: historyPoints(byKey), loading: false, error: null, at: Date.now() })
      } catch (e) {
        if (ctrl.signal.aborted) return
        const msg = e instanceof HistoryDisabledError ? e.message : `Couldn't load history: ${e instanceof Error ? e.message : e}`
        setState((s) => ({ ...s, loading: false, error: msg }))
      }
    }
    load()
    const id = setInterval(load, refreshMs)
    return () => {
      clearInterval(id)
      ctrl.abort()
    }
  }, [active])

  return state
}
