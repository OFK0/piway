import { useLayoutEffect, useState } from 'react'

import { msUntilNextSecond, secondsLeft } from '@/lib/countdown'

/**
 * Whole seconds until deadline (epoch ms), or 0 without one. It re-renders
 * exactly when the number changes, and a new deadline shows its own first
 * second at once instead of one computed from an old clock reading.
 */
export function useCountdown(deadline: number | null): number {
  const [now, setNow] = useState(() => Date.now())

  // A layout effect, so a new deadline's clock reading lands before paint:
  // the first number shown is already right.
  useLayoutEffect(() => {
    if (deadline === null) return
    let id: ReturnType<typeof setTimeout> | undefined
    const tick = () => {
      const t = Date.now()
      setNow(t)
      const wait = msUntilNextSecond(deadline, t)
      if (wait !== null) id = setTimeout(tick, wait)
    }
    tick()
    return () => clearTimeout(id)
  }, [deadline])

  return deadline === null ? 0 : secondsLeft(deadline, now)
}
