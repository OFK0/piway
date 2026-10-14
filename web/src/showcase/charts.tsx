import { CartesianGrid, Line, LineChart, ReferenceLine, XAxis, YAxis } from 'recharts'

import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from '@/components/ui/chart'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Block, Section } from '@/showcase/section'

// Deterministic sample data: a minute of 5-second samples.
const samples = Array.from({ length: 13 }, (_, i) => {
  const t = i * 5
  return {
    time: `${t}s`,
    cpu: Math.round(28 + 18 * Math.sin(i / 1.7) + (i === 9 ? 46 : 0)),
    rx: Math.round(420 + 260 * Math.sin(i / 2.2) + i * 18),
    tx: Math.round(160 + 90 * Math.cos(i / 1.9)),
  }
})

const cpuPeak = samples.reduce((a, b) => (b.cpu > a.cpu ? b : a))

const cpuConfig = {
  cpu: { label: 'CPU', color: 'var(--chart-1)' },
} satisfies ChartConfig

const netConfig = {
  rx: { label: 'Receive', color: 'var(--chart-1)' },
  tx: { label: 'Send', color: 'var(--chart-2)' },
} satisfies ChartConfig

const axis = { tickLine: false, axisLine: false, tickMargin: 8 } as const

export function Charts() {
  return (
    <Section
      id="charts"
      title="Data visualization"
      description="Recharts through shadcn's Chart. One hue means at most two series per chart; more series become small multiples."
    >
      <div className="grid gap-6 lg:grid-cols-2">
        <Block title="Single series" description="No legend: the title names the series. Thresholds are neutral and dashed.">
          <figure className="flex flex-col gap-3">
            <figcaption className="type-label">CPU, last minute (%)</figcaption>
            <ChartContainer config={cpuConfig} className="aspect-auto h-56 w-full" role="img" aria-label={`CPU usage over the last minute, peaking at ${cpuPeak.cpu}% at ${cpuPeak.time}`}>
              <LineChart data={samples} margin={{ left: 0, right: 12, top: 8 }} accessibilityLayer>
                <CartesianGrid vertical={false} />
                <XAxis dataKey="time" {...axis} interval={2} />
                <YAxis {...axis} width={36} domain={[0, 100]} ticks={[0, 25, 50, 75, 100]} />
                <ReferenceLine
                  y={90}
                  stroke="var(--chart-3)"
                  strokeDasharray="4 3"
                  label={{ value: 'Saturated, 90%', position: 'insideTopRight', fill: 'var(--muted-foreground)', fontSize: 12 }}
                />
                <ChartTooltip cursor={{ stroke: 'var(--muted-foreground)' }} content={<ChartTooltipContent indicator="line" />} />
                <Line dataKey="cpu" type="monotone" stroke="var(--color-cpu)" strokeWidth={2} dot={false} activeDot={{ r: 4 }} isAnimationActive={false} />
              </LineChart>
            </ChartContainer>
          </figure>
        </Block>

        <Block
          title="Two series"
          description="Raspberry solid and ink dashed: hue, lightness and stroke all differ, so the pair survives color blindness and print."
        >
          <figure className="flex flex-col gap-3">
            <figcaption className="type-label">Network, last minute (KiB/s)</figcaption>
            <ChartContainer config={netConfig} className="aspect-auto h-56 w-full" role="img" aria-label="Network receive and send rates over the last minute">
              <LineChart data={samples} margin={{ left: 0, right: 12, top: 8 }} accessibilityLayer>
                <CartesianGrid vertical={false} />
                <XAxis dataKey="time" {...axis} interval={2} />
                <YAxis {...axis} width={44} />
                <ChartTooltip cursor={{ stroke: 'var(--muted-foreground)' }} content={<ChartTooltipContent indicator="line" />} />
                <ChartLegend content={<ChartLegendContent />} />
                <Line dataKey="rx" type="monotone" stroke="var(--color-rx)" strokeWidth={2} dot={false} activeDot={{ r: 4 }} isAnimationActive={false} />
                <Line dataKey="tx" type="monotone" stroke="var(--color-tx)" strokeWidth={2} strokeDasharray="4 3" dot={false} activeDot={{ r: 4 }} isAnimationActive={false} />
              </LineChart>
            </ChartContainer>
            <details className="type-caption text-muted-foreground">
              <summary className="w-fit cursor-pointer">Show as table</summary>
              <div className="mt-2 max-h-48 overflow-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Time</TableHead>
                      <TableHead className="text-right">Receive</TableHead>
                      <TableHead className="text-right">Send</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {samples.map((s) => (
                      <TableRow key={s.time}>
                        <TableCell>{s.time}</TableCell>
                        <TableCell className="text-right tabular-nums">{s.rx}</TableCell>
                        <TableCell className="text-right tabular-nums">{s.tx}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </details>
          </figure>
        </Block>
      </div>
    </Section>
  )
}
