import { animate, useReducedMotion } from 'motion/react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'

import { cn } from '@/lib/utils'

/**
 * How long a number takes to reach a new value. It stays well under the
 * default 1 s sample interval, so each value settles before the next one.
 */
export const numberTween = { duration: 0.6, ease: 'easeOut' } as const

export interface AnimatedNumberProps {
  value: number
  /** Formats every frame, including the unit if the number carries one. */
  format: (n: number) => string
  className?: string
}

// A number that glides to each new value (DESIGN.md › Motion). The first
// render shows the value as is; only changes animate. A change during an
// animation starts from the number on screen, so nothing jumps.
//
// Frames are written straight to the DOM instead of through React state, so a
// tween costs no re-renders. React never touches the text after the first
// render because the child below never changes.
export function AnimatedNumber({ value, format, className }: AnimatedNumberProps) {
  const ref = useRef<HTMLSpanElement>(null)
  const [initial] = useState(() => format(value))
  const shown = useRef(value)
  const formatRef = useRef(format)
  const reduce = useReducedMotion()

  // A new format (say, a different unit) applies to the number on screen
  // right away, mid-tween or not.
  useLayoutEffect(() => {
    formatRef.current = format
    if (ref.current) ref.current.textContent = format(shown.current)
  })

  useEffect(() => {
    const el = ref.current
    if (!el) return
    const write = (n: number) => {
      shown.current = n
      el.textContent = formatRef.current(n)
    }
    if (reduce || shown.current === value || !Number.isFinite(shown.current) || !Number.isFinite(value)) {
      write(value)
      return
    }
    const controls = animate(shown.current, value, { ...numberTween, onUpdate: write })
    return () => controls.stop()
  }, [value, reduce])

  return (
    <span ref={ref} className={cn('tabular-nums', className)}>
      {initial}
    </span>
  )
}
