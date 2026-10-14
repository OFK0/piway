import type { Icon } from '@phosphor-icons/react'
import type { ReactNode } from 'react'

import { StatusBadge } from '@/components/status-badge'
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { Status } from '@/lib/metrics'
import { cn } from '@/lib/utils'

export interface MetricCardProps {
  title: string
  icon: Icon
  /** Formatted number, or null when the metric is missing. */
  value: string | null
  unit?: string
  /** Shown instead of the value when it is null. */
  missingReason?: string
  status?: Status
  /** 0–100; renders a meter under the value. */
  meter?: number
  meterLabel?: string
  children?: ReactNode
}

export function MetricCard(props: MetricCardProps) {
  const { icon: Icon, status } = props
  const critical = status?.level === 'critical'

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 type-label text-muted-foreground">
          <Icon aria-hidden="true" />
          {props.title}
        </CardTitle>
        {status && (
          <CardAction>
            <StatusBadge status={status} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {props.value === null ? (
          <Missing reason={props.missingReason} />
        ) : (
          <p className="flex items-baseline gap-1">
            <span className="type-metric">{props.value}</span>
            {props.unit && <span className="type-label text-muted-foreground">{props.unit}</span>}
          </p>
        )}
        {props.meter !== undefined && props.value !== null && (
          <Progress
            value={props.meter}
            aria-label={props.meterLabel ?? props.title}
            className={cn(critical && '**:data-[slot=progress-indicator]:bg-destructive')}
          />
        )}
        {props.children && <div className="type-caption text-muted-foreground">{props.children}</div>}
      </CardContent>
    </Card>
  )
}

function Missing({ reason }: { reason?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={<button type="button" className="w-fit cursor-help type-metric text-muted-foreground" />}
        aria-label={reason ?? 'Not available'}
      >
        —
      </TooltipTrigger>
      <TooltipContent>{reason ?? 'Not available'}</TooltipContent>
    </Tooltip>
  )
}

export function MetricCardSkeleton() {
  return (
    <Card aria-hidden="true">
      <CardHeader>
        <Skeleton className="h-4 w-24" />
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Skeleton className="h-8 w-28" />
        <Skeleton className="h-1 w-full" />
        <Skeleton className="h-3 w-20" />
      </CardContent>
    </Card>
  )
}
