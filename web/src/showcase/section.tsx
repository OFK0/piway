import type { ReactNode } from 'react'

import { Separator } from '@/components/ui/separator'

export function Section(props: { id: string; title: string; description: string; children: ReactNode }) {
  return (
    <section id={props.id} aria-labelledby={`${props.id}-title`} className="flex scroll-mt-20 flex-col gap-6">
      <Separator />
      <div className="flex max-w-2xl flex-col gap-1">
        <h2 id={`${props.id}-title`} className="type-display">
          {props.title}
        </h2>
        <p className="text-muted-foreground">{props.description}</p>
      </div>
      {props.children}
    </section>
  )
}

export function Block(props: { title: string; description?: string; children: ReactNode; wide?: boolean }) {
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex flex-col gap-0.5">
        <h3 className="type-title">{props.title}</h3>
        {props.description && <p className="max-w-2xl text-muted-foreground">{props.description}</p>}
      </div>
      <div className="min-w-0 border p-4 md:p-6">{props.children}</div>
    </div>
  )
}
