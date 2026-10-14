import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ColorPair } from '@/showcase/color-pair'
import { Block, Section } from '@/showcase/section'

const roles = [
  { name: 'Canvas', background: 'background', foreground: 'foreground' },
  { name: 'Card', background: 'card', foreground: 'card-foreground' },
  { name: 'Popover', background: 'popover', foreground: 'popover-foreground' },
  { name: 'Primary action', background: 'primary', foreground: 'primary-foreground' },
  { name: 'Brand', background: 'brand', foreground: 'brand-foreground' },
  { name: 'Secondary', background: 'secondary', foreground: 'secondary-foreground' },
  { name: 'Muted', background: 'muted', foreground: 'muted-foreground' },
  { name: 'Hover wash', background: 'accent', foreground: 'accent-foreground' },
  { name: 'Secondary text', background: 'background', foreground: 'muted-foreground' },
  { name: 'Brand text', background: 'background', foreground: 'brand-text' },
  { name: 'Critical', background: 'background', foreground: 'destructive' },
] as const

const swatches = [
  { group: 'Brand', items: ['brand', 'brand-text', 'ring', 'destructive'] },
  { group: 'Ink and surfaces', items: ['foreground', 'background', 'card', 'muted', 'accent', 'muted-foreground'] },
  { group: 'Lines', items: ['border', 'input'] },
  { group: 'Chart', items: ['chart-1', 'chart-2', 'chart-3', 'chart-4', 'chart-5'] },
]

const typeScale = [
  { token: 'type-display', spec: '28–36 / 1.1 / 600 / −0.02em', use: 'Page titles', sample: 'Overview' },
  { token: 'type-metric', spec: '24–32 / 1.1 / 500 / tabular', use: 'Metric values', sample: '42.5 °C' },
  { token: 'type-title', spec: '18 / 1.3 / 600 / −0.01em', use: 'Section headings', sample: 'Network' },
  { token: 'type-body', spec: '14 / 1.5 / 400', use: 'Running text, cells', sample: 'Throughput since the previous sample.' },
  { token: 'type-label', spec: '13 / 1.4 / 500', use: 'Card titles, labels', sample: 'Temperature' },
  { token: 'type-caption', spec: '12 / 1.4 / 400', use: 'Units, helper text', sample: 'Throttling starts at 80 °C' },
  { token: 'type-code', spec: '13 / 1.5 / Plex Mono', use: 'Code, logs, IDs', sample: 'GET /api/v1/snapshot' },
]

const spacing = [
  { size: 1, px: 4, use: 'Icon to label' },
  { size: 2, px: 8, use: 'Related items' },
  { size: 3, px: 12, use: 'Inside a card' },
  { size: 4, px: 16, use: 'Between groups, card padding' },
  { size: 8, px: 32, use: 'Between sections' },
]

export function Foundations() {
  return (
    <Section
      id="foundations"
      title="Foundations"
      description="Ink, white and neutral gray, with raspberry as the only hue. Raspberry means attention, so it is rare."
    >
      <Block
        title="Color roles"
        description="Each surface with its foreground and the contrast measured live in the current theme. Body text needs 4.5:1."
      >
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {roles.map((r) => (
            <ColorPair key={r.name} {...r} />
          ))}
        </div>
      </Block>

      <Block title="Palette" description="Tokens by group. Components use these names, never raw values.">
        <div className="flex flex-col gap-6">
          {swatches.map((g) => (
            <div key={g.group} className="flex flex-col gap-2">
              <h4 className="type-label">{g.group}</h4>
              <ul className="grid grid-cols-2 gap-3 sm:grid-cols-4 lg:grid-cols-6">
                {g.items.map((token) => (
                  <li key={token} className="flex flex-col gap-1.5">
                    <span className="h-12 border" style={{ background: `var(--${token})` }} />
                    <code className="type-code text-muted-foreground">--{token}</code>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </Block>

      <Block title="Typography" description="IBM Plex Sans for text and numbers, IBM Plex Mono for code. Sentence case everywhere.">
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Token</TableHead>
                <TableHead>Size / leading / weight</TableHead>
                <TableHead>Use</TableHead>
                <TableHead>Specimen</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {typeScale.map((t) => (
                <TableRow key={t.token}>
                  <TableCell className="type-code">{t.token}</TableCell>
                  <TableCell className="text-muted-foreground tabular-nums">{t.spec}</TableCell>
                  <TableCell className="text-muted-foreground">{t.use}</TableCell>
                  <TableCell className="max-w-72 truncate">
                    <span className={t.token}>{t.sample}</span>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
        <p className="mt-6 max-w-[70ch]">
          piway keeps a Raspberry Pi healthy and runs your apps on it. Values update in place with tabular figures, so{' '}
          <span className="tabular-nums">1,204.7 KiB/s</span> never shifts its neighbours. Commands look like{' '}
          <code className="bg-muted px-1 whitespace-nowrap type-code">piway -print-config</code>, and{' '}
          <a href="#components" className="underline underline-offset-4 hover:text-brand-text">
            links are underlined
          </a>
          .
        </p>
      </Block>

      <div className="grid gap-6 lg:grid-cols-2">
        <Block title="Spacing" description="A 4px base unit.">
          <ul className="flex flex-col gap-3">
            {spacing.map((s) => (
              <li key={s.size} className="grid grid-cols-[3rem_1fr] items-center gap-3">
                <span className="type-code text-muted-foreground">{s.px}px</span>
                <span className="flex items-center gap-3">
                  <span className="h-3 bg-foreground" style={{ width: s.px * 2 }} />
                  <span className="text-muted-foreground">{s.use}</span>
                </span>
              </li>
            ))}
          </ul>
        </Block>
        <Block title="Shape and elevation" description="0 radius everywhere. Hairlines separate regions; only floating layers cast a shadow.">
          <div className="grid grid-cols-3 gap-4">
            <div className="flex flex-col gap-2">
              <div className="h-16 bg-muted" />
              <span className="type-caption text-muted-foreground">Flat: canvas, bands</span>
            </div>
            <div className="flex flex-col gap-2">
              <div className="h-16 border bg-card" />
              <span className="type-caption text-muted-foreground">Hairline: cards, tables</span>
            </div>
            <div className="flex flex-col gap-2">
              <div className="h-16 bg-popover shadow-lg ring-1 ring-border" />
              <span className="type-caption text-muted-foreground">Floating: menus, dialogs</span>
            </div>
          </div>
        </Block>
      </div>
    </Section>
  )
}
