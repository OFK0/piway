import { useCallback, useEffect, useState } from 'react'

import { fetchHealth, fetchSnapshot, type Health, type Snapshot } from '@/api'

export interface SnapshotState {
  snapshot: Snapshot | null
  health: Health | null
  error: string | null
  loading: boolean
  /** When the shown snapshot was received. */
  receivedAt: Date | null
  refresh: () => void
}

// Fetches the latest snapshot on mount and on refresh(). The previous data
// stays on screen while a refresh is in flight, so the page never blanks.
// #13 replaces this with the WebSocket stream behind the same shape.
export function useSnapshot(): SnapshotState {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [health, setHealth] = useState<Health | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [receivedAt, setReceivedAt] = useState<Date | null>(null)
  // Each refresh bumps the request number; the effect below does the fetch.
  const [request, setRequest] = useState(0)

  const refresh = useCallback(() => {
    setLoading(true)
    setRequest((n) => n + 1)
  }, [])

  useEffect(() => {
    const ctrl = new AbortController()
    Promise.all([fetchSnapshot(ctrl.signal), fetchHealth(ctrl.signal)])
      .then(([s, h]) => {
        setSnapshot(s)
        setHealth(h)
        setReceivedAt(new Date())
        setError(null)
      })
      .catch((e: unknown) => {
        if (ctrl.signal.aborted) return
        setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false)
      })
    // A newer refresh or unmount cancels this request.
    return () => ctrl.abort()
  }, [request])

  return { snapshot, health, error, loading, receivedAt, refresh }
}
