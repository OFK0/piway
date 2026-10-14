// Whole-second countdowns that change exactly when the second does.

/** Seconds left until deadline, rounded up: 2.3 s left reads "3 s". */
export function secondsLeft(deadline: number, now: number): number {
  return Math.max(0, Math.ceil((deadline - now) / 1000))
}

/**
 * Milliseconds until secondsLeft() drops by one, or null once the deadline
 * has passed. Waking then (plus 1 ms, to land past the boundary) makes every
 * displayed second last exactly one second.
 */
export function msUntilNextSecond(deadline: number, now: number): number | null {
  const left = deadline - now
  if (left <= 0) return null
  return (left % 1000 || 1000) + 1
}
