import { CheckCircleIcon, ClockIcon, ProhibitIcon, WarningCircleIcon } from '@phosphor-icons/react'

import type { Build } from '@/api'
import { Badge } from '@/components/ui/badge'
import { Spinner } from '@/components/ui/spinner'
import { capitalize, doneVerb, runningVerb } from '@/lib/build-labels'

// Icon + word for every state (DESIGN.md › Principles); raspberry only for
// a failure, the one state that needs attention.
export function BuildState({ build }: { build: Pick<Build, 'state' | 'kind'> }) {
  switch (build.state) {
    case 'running':
      return (
        <Badge variant="outline">
          <Spinner data-icon="inline-start" aria-hidden />
          {runningVerb(build)}
        </Badge>
      )
    case 'queued':
      return (
        <Badge variant="outline">
          <ClockIcon data-icon="inline-start" />
          Queued
        </Badge>
      )
    case 'succeeded':
      return (
        <Badge variant="secondary">
          <CheckCircleIcon data-icon="inline-start" />
          {capitalize(doneVerb(build))}
        </Badge>
      )
    case 'failed':
      return (
        <Badge variant="destructive">
          <WarningCircleIcon data-icon="inline-start" weight="bold" />
          Failed
        </Badge>
      )
    case 'cancelled':
      return (
        <Badge variant="outline">
          <ProhibitIcon data-icon="inline-start" />
          Cancelled
        </Badge>
      )
  }
}
