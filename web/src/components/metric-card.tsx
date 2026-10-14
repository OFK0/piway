import type { Icon } from '@phosphor-icons/react'
import type { ReactNode } from 'react'

import { StatusBadge } from '@/components/status-badge'
import { Card, CardAction, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Skeleton, SkeletonText } from '@/components/ui/skeleton'
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

// CardAction is always present, even empty: it gives the header its height,
// so a badge that appears or clears, or a skeleton turning into a card, does
// not move the value.
const titleClass = 'flex items-center gap-2 type-label text-muted-foreground'

export function MetricCard(props: MetricCardProps) {
  const { icon: Icon, status } = props
  const critical = status?.level === 'critical'

  return (
    <Card>
      <CardHeader>
        <CardTitle className={titleClass}>
          <Icon aria-hidden="true" />
          {props.title}
        </CardTitle>
        <CardAction>{status && <StatusBadge status={status} />}</CardAction>
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

export interface MetricCardSkeletonProps {
  /** The real title and icon; without them the header is a placeholder too. */
  title?: string
  icon?: Icon
  /** Whether the loaded card has a meter under the value. */
  meter?: boolean
  /** Placeholder for the caption; defaults to one line of text. */
  children?: ReactNode
}

// A MetricCard whose numbers are still loading. Everything known before the
// data arrives is real, and each placeholder has the line height of the text
// it stands for, so the card keeps its size when the values come in.
export function MetricCardSkeleton(props: MetricCardSkeletonProps) {
  const { icon: Icon } = props
  return (
    <Card aria-hidden="true">
      <CardHeader>
        <CardTitle className={titleClass}>
          {Icon && props.title ? (
            <>
              <Icon />
              {props.title}
            </>
          ) : (
            <SkeletonText>Temperature</SkeletonText>
          )}
        </CardTitle>
        <CardAction />
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <p className="flex items-baseline gap-1">
          <SkeletonText className="type-metric">00.0</SkeletonText>
          {/* The unit's baseline makes the line a few pixels taller. */}
          <span className="invisible type-label">%</span>
        </p>
        {props.meter && <Skeleton className="h-1 w-full" />}
        <div className="type-caption">{props.children ?? <SkeletonText>of 0.0 GiB, 0.0 GiB free</SkeletonText>}</div>
      </CardContent>
    </Card>
  )
}
