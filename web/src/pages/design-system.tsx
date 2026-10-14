import { AppShell } from '@/components/app-shell'
import { Charts } from '@/showcase/charts'
import { Components } from '@/showcase/components'
import { Foundations } from '@/showcase/foundations'
import { Guidelines } from '@/showcase/guidelines'

const sections = [
  { id: 'foundations', label: 'Foundations' },
  { id: 'components', label: 'Components' },
  { id: 'charts', label: 'Data visualization' },
  { id: 'guidelines', label: 'Do and don’t' },
]

// The rendered reference for web/DESIGN.md. Every token and component the
// dashboard uses appears here, in both themes (toggle in the header).
export default function DesignSystemPage() {
  return (
    <AppShell>
      <div className="flex flex-col gap-4">
        <div className="flex max-w-2xl flex-col gap-2">
          <h1 className="type-display">Design system</h1>
          <p className="text-muted-foreground">
            The tokens and components piway's dashboard is built from. The spec lives in{' '}
            <code className="type-code text-foreground">web/DESIGN.md</code>; this page renders it.
          </p>
        </div>
        <nav aria-label="Sections">
          <ul className="flex flex-wrap gap-x-4 gap-y-1">
            {sections.map((s) => (
              <li key={s.id}>
                <a href={`#${s.id}`} className="underline-offset-4 hover:underline">
                  {s.label}
                </a>
              </li>
            ))}
          </ul>
        </nav>
      </div>
      <Foundations />
      <Components />
      <Charts />
      <Guidelines />
    </AppShell>
  )
}
