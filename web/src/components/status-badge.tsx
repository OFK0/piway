import { WarningCircleIcon, WarningIcon } from '@phosphor-icons/react'

import { Badge } from '@/components/ui/badge'
import type { Status } from '@/lib/metrics'

// Icon + word + color, never color alone. "ok" renders nothing: calm is the default.
export function StatusBadge({ status }: { status: Status }) {
  if (status.level === 'ok') return null
  if (status.level === 'critical') {
    return (
      <Badge variant="destructive">
        <WarningCircleIcon data-icon="inline-start" weight="bold" />
        {status.label}
      </Badge>
    )
  }
  return (
    <Badge variant="outline">
      <WarningIcon data-icon="inline-start" weight="bold" />
      {status.label}
    </Badge>
  )
}
