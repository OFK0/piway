import { ArrowsClockwiseIcon, WifiSlashIcon } from '@phosphor-icons/react'

import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useCountdown } from '@/hooks/use-countdown'
import type { LiveState } from '@/lib/live-stream'

// The live indicator (DESIGN.md › Principles: raspberry means "look here").
// Text always states the connection, so it never relies on the dot's color.
export function ConnectionStatus({ state, onRetry }: { state: LiveState; onRetry: () => void }) {
  const reconnecting = state.status === 'reconnecting'
  const seconds = useCountdown(reconnecting ? state.retryAt : null)

  return (
    <div className="flex items-center gap-2 type-label">
      {/* Announce changes of state, not every countdown tick. */}
      <span className="sr-only" aria-live="polite">
        {label(state.status)}
      </span>
      <span aria-hidden="true" className="flex items-center gap-2">
        {state.status === 'live' && (
          <>
            <span className="relative flex size-2">
              <span className="absolute inline-flex size-full animate-ping rounded-full bg-brand opacity-60" />
              <span className="relative inline-flex size-2 rounded-full bg-brand" />
            </span>
            Live
          </>
        )}
        {state.status === 'connecting' && (
          <>
            <Spinner aria-hidden />
            Connecting…
          </>
        )}
        {reconnecting && (
          <>
            <ArrowsClockwiseIcon />
            <span className="tabular-nums">
              {seconds > 0 ? `Reconnecting in ${seconds} s` : 'Reconnecting…'}
            </span>
          </>
        )}
        {state.status === 'offline' && (
          <>
            <WifiSlashIcon />
            Offline
          </>
        )}
      </span>
      {reconnecting && (
        <Button variant="ghost" size="sm" onClick={onRetry}>
          Retry now
        </Button>
      )}
    </div>
  )
}

function label(status: LiveState['status']): string {
  switch (status) {
    case 'live':
      return 'Live: receiving updates.'
    case 'connecting':
      return 'Connecting to piway.'
    case 'reconnecting':
      return 'Connection lost. Reconnecting.'
    case 'offline':
      return 'Offline. Waiting for the network.'
  }
}
