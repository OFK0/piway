import { useEffect, useState } from 'react'

import { fetchHealth, type Health } from '@/api'

/** Version info from /healthz, fetched once. Null until it arrives. */
export function useHealth(): Health | null {
  const [health, setHealth] = useState<Health | null>(null)
  useEffect(() => {
    const ctrl = new AbortController()
    fetchHealth(ctrl.signal)
      .then(setHealth)
      .catch(() => {}) // the version is decoration; the stream reports connectivity
    return () => ctrl.abort()
  }, [])
  return health
}
