import { CheckIcon, WarningCircleIcon, XIcon } from '@phosphor-icons/react'
import type { ReactNode } from 'react'

import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Block, Section } from '@/showcase/section'

function Pair(props: { rule: string; good: ReactNode; bad: ReactNode }) {
  return (
    <Block title={props.rule}>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="flex flex-col gap-3">
          <span className="flex items-center gap-1.5 type-label">
            <CheckIcon aria-hidden="true" weight="bold" />
            Do
          </span>
          <div className="flex min-h-16 flex-wrap items-center gap-3">{props.good}</div>
        </div>
        <div className="flex flex-col gap-3">
          <span className="flex items-center gap-1.5 type-label text-destructive">
            <XIcon aria-hidden="true" weight="bold" />
            Don't
          </span>
          <div className="flex min-h-16 flex-wrap items-center gap-3">{props.bad}</div>
        </div>
      </div>
    </Block>
  )
}

export function Guidelines() {
  return (
    <Section id="guidelines" title="Do and don't" description="The rules from DESIGN.md that are easiest to break.">
      <div className="grid gap-6 lg:grid-cols-2">
        <Pair
          rule="One primary action per group."
          good={
            <>
              <Button>Save</Button>
              <Button variant="outline">Cancel</Button>
            </>
          }
          bad={
            <>
              <Button>Save</Button>
              <Button>Cancel</Button>
            </>
          }
        />
        <Pair
          rule="Status is an icon and a word, not a dot."
          good={<StatusBadge status={{ level: 'critical', label: 'Under-voltage' }} />}
          bad={<span aria-label="status" className="size-2.5 rounded-full bg-destructive" />}
        />
        <Pair
          rule="Raspberry only where attention is needed."
          good={
            <p className="flex items-center gap-2">
              <WarningCircleIcon className="text-destructive" aria-hidden="true" />
              <span>
                Disk is <strong className="font-semibold text-destructive">93% full</strong>
              </span>
            </p>
          }
          bad={<p className="type-title text-brand-text">Network</p>}
        />
        <Pair
          rule="A missing value is a dash with a reason, never 0."
          good={<span className="type-metric text-muted-foreground" title="Needs a second sample">—</span>}
          bad={
            <span className="flex items-baseline gap-1">
              <span className="type-metric">0.0</span>
              <span className="text-muted-foreground">%</span>
            </span>
          }
        />
      </div>
    </Section>
  )
}
