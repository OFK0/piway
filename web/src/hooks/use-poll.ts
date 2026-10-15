import { useCallback, useEffect, useState } from 'react'

export interface Poll<T> {
  data: T | null
  error: unknown
  loading: boolean
  reload: () => void
}

/**
 * Fetches with load() now and every everyMs, keeping the last data while a
 * reload is in flight. A failure is kept in error, not thrown.
 */
export function usePoll<T>(load: (signal: AbortSignal) => Promise<T>, everyMs: number): Poll<T> {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [loading, setLoading] = useState(true)
  const [tick, setTick] = useState(0)
  const reload = useCallback(() => setTick((n) => n + 1), [])

  useEffect(() => {
    const ctrl = new AbortController()
    load(ctrl.signal)
      .then((d) => {
        setData(d)
        setError(null)
      })
      .catch((e) => {
        if (!ctrl.signal.aborted) setError(e)
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false)
      })
    const id = setTimeout(reload, everyMs)
    return () => {
      ctrl.abort()
      clearTimeout(id)
    }
  }, [load, everyMs, tick, reload])

  return { data, error, loading, reload }
}
