import { useMemo } from 'react'
import { CartesianGrid, Line, LineChart, ReferenceLine, XAxis, YAxis } from 'recharts'

import type { Snapshot } from '@/api'
import { AnimatedNumber } from '@/components/animated-number'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from '@/components/ui/chart'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'

// Live charts for the overview (DESIGN.md › Data visualization): at most
// two series per chart, raspberry solid then ink dashed, one y-axis,
// thresholds as neutral dashed lines, values in text tokens.

export const ranges = { '1m': 60_000, '5m': 300_000 } as const
export type Range = keyof typeof ranges

interface Point {
  t: number
  cpu: number | null
  mem: number | null
  temp: number | null
  rx: number | null
  tx: number | null
}

function toPoint(s: Snapshot): Point {
  const net = s.network
  return {
    t: Date.parse(s.time),
    cpu: s.cpu?.total ?? null,
    mem: s.memory ? (s.memory.used_bytes / s.memory.total_bytes) * 100 : null,
    temp: s.thermal?.temp_celsius ?? null,
    rx: net ? net.reduce((a, n) => a + n.rx_bytes_per_sec, 0) : null,
    tx: net ? net.reduce((a, n) => a + n.tx_bytes_per_sec, 0) : null,
  }
}

const timeFormat = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })
const fmtTime = (t: number) => timeFormat.format(t)

// Picks one unit for the whole visible window so the axis reads "KiB/s",
// not a mix of units or long decimals.
function rateScale(max: number): { div: number; unit: string } {
  if (max >= 1024 * 1024) return { div: 1024 * 1024, unit: 'MiB/s' }
  if (max >= 1024) return { div: 1024, unit: 'KiB/s' }
  return { div: 1, unit: 'B/s' }
}

function summary(name: string, unit: string, values: (number | null)[], range: Range): string {
  const v = values.filter((x): x is number => x !== null)
  if (v.length === 0) return `${name}: no data yet.`
  const f = (x: number) => `${x.toFixed(1)}${unit}`
  return `${name} over the last ${range === '1m' ? 'minute' : '5 minutes'}: now ${f(v[v.length - 1])}, low ${f(Math.min(...v))}, high ${f(Math.max(...v))}.`
}

export default function LiveCharts(props: { history: Snapshot[]; range: Range; onRangeChange: (r: Range) => void }) {
  const { history, range } = props

  const points = useMemo(() => {
    const all = history.map(toPoint)
    const end = all.length ? all[all.length - 1].t : 0
    return all.filter((p) => p.t >= end - ranges[range])
  }, [history, range])

  const net = useMemo(() => {
    const max = Math.max(0, ...points.flatMap((p) => [p.rx ?? 0, p.tx ?? 0]))
    const { div, unit } = rateScale(max)
    return {
      unit,
      data: points.map((p) => ({ t: p.t, rx: p.rx === null ? null : p.rx / div, tx: p.tx === null ? null : p.tx / div })),
    }
  }, [points])

  const now = points[points.length - 1]

  return (
    <section aria-labelledby="trends-title" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="trends-title" className="type-title">
          Trends
        </h2>
        <ToggleGroup
          variant="outline"
          size="sm"
          value={[range]}
          onValueChange={(v: string[]) => v[0] && props.onRangeChange(v[0] as Range)}
          aria-label="Time range"
        >
          <ToggleGroupItem value="1m">1 min</ToggleGroupItem>
          <ToggleGroupItem value="5m">5 min</ToggleGroupItem>
        </ToggleGroup>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <ChartCard
          title="CPU"
          unit="%"
          current={now?.cpu}
          label={summary('CPU', '%', points.map((p) => p.cpu), range)}
          config={{ v: { label: 'CPU', color: 'var(--chart-1)' } }}
          data={points.map((p) => ({ t: p.t, v: p.cpu }))}
          domain={[0, 100]}
          threshold={{ y: 90, label: 'Saturated, 90%' }}
        />
        <ChartCard
          title="Temperature"
          unit="°C"
          current={now?.temp}
          label={summary('Temperature', ' °C', points.map((p) => p.temp), range)}
          config={{ v: { label: 'Temperature', color: 'var(--chart-1)' } }}
          data={points.map((p) => ({ t: p.t, v: p.temp }))}
          domain={[tempFloor(points), 90]}
          threshold={{ y: 80, label: 'Throttling, 80 °C' }}
        />
        <ChartCard
          title="Memory"
          unit="%"
          current={now?.mem}
          label={summary('Memory used', '%', points.map((p) => p.mem), range)}
          config={{ v: { label: 'Used', color: 'var(--chart-1)' } }}
          data={points.map((p) => ({ t: p.t, v: p.mem }))}
          domain={[0, 100]}
        />
        <NetworkCard data={net.data} unit={net.unit} label={summary('Network receive', ` ${net.unit}`, net.data.map((p) => p.rx), range)} />
      </div>
    </section>
  )
}

const axis = { tickLine: false, axisLine: false, tickMargin: 8 } as const

// Temperature axis: 40–90 °C normally, extended down in steps of 10 for a
// cold board so the line never leaves the plot.
function tempFloor(points: Point[]): number {
  const lo = Math.min(40, ...points.map((p) => p.temp ?? 40))
  return Math.floor(lo / 10) * 10
}

// Five or six evenly spaced ticks on round numbers.
function evenTicks([lo, hi]: [number, number]): number[] {
  const step = hi - lo > 60 ? 25 : 10
  const out: number[] = []
  for (let v = Math.ceil(lo / step) * step; v <= hi; v += step) out.push(v)
  return out
}

function xAxis() {
  return (
    <XAxis
      dataKey="t"
      type="number"
      scale="time"
      domain={['dataMin', 'dataMax']}
      tickFormatter={fmtTime}
      minTickGap={48}
      {...axis}
    />
  )
}

function tooltip(unit: string) {
  return (
    <ChartTooltip
      cursor={{ stroke: 'var(--muted-foreground)' }}
      content={
        <ChartTooltipContent
          indicator="line"
          labelFormatter={(_, payload) => fmtTime(Number(payload?.[0]?.payload?.t))}
          formatter={(value, name) => (
            <span className="flex w-full justify-between gap-4">
              <span className="text-muted-foreground">{name}</span>
              <span className="font-medium tabular-nums">
                {typeof value === 'number' ? value.toFixed(1) : '—'} {unit}
              </span>
            </span>
          )}
        />
      }
    />
  )
}

function ChartCard(props: {
  title: string
  unit: string
  current: number | null | undefined
  label: string
  config: ChartConfig
  data: { t: number; v: number | null }[]
  domain: [number, number]
  threshold?: { y: number; label: string }
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="type-label">{props.title}</CardTitle>
        <CardAction className="type-label tabular-nums">
          {props.current == null ? (
            '—'
          ) : (
            <AnimatedNumber value={props.current} format={(n) => `${n.toFixed(1)}\u00a0${props.unit}`} />
          )}
        </CardAction>
      </CardHeader>
      <CardContent>
        <ChartContainer config={props.config} className="aspect-auto h-48 w-full" role="img" aria-label={props.label}>
          <LineChart data={props.data} margin={{ left: 0, right: 12, top: 8 }} accessibilityLayer>
            <CartesianGrid vertical={false} />
            {xAxis()}
            <YAxis {...axis} width={36} domain={props.domain} ticks={evenTicks(props.domain)} allowDataOverflow />
            {props.threshold && (
              <ReferenceLine
                y={props.threshold.y}
                stroke="var(--chart-3)"
                strokeDasharray="4 3"
                label={{ value: props.threshold.label, position: 'insideTopRight', fill: 'var(--muted-foreground)', fontSize: 12 }}
              />
            )}
            {tooltip(props.unit)}
            <Line dataKey="v" type="monotone" stroke="var(--color-v)" strokeWidth={2} dot={false} activeDot={{ r: 4 }} isAnimationActive={false} />
          </LineChart>
        </ChartContainer>
      </CardContent>
    </Card>
  )
}

const netConfig = {
  rx: { label: 'Receive', color: 'var(--chart-1)' },
  tx: { label: 'Send', color: 'var(--chart-2)' },
} satisfies ChartConfig

function NetworkCard(props: { data: { t: number; rx: number | null; tx: number | null }[]; unit: string; label: string }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="type-label">Network</CardTitle>
        <CardDescription>All interfaces, {props.unit}</CardDescription>
      </CardHeader>
      <CardContent>
        <ChartContainer config={netConfig} className="aspect-auto h-48 w-full" role="img" aria-label={props.label}>
          <LineChart data={props.data} margin={{ left: 0, right: 12, top: 8 }} accessibilityLayer>
            <CartesianGrid vertical={false} />
            {xAxis()}
            <YAxis {...axis} width={44} />
            {tooltip(props.unit)}
            <ChartLegend content={<ChartLegendContent />} />
            <Line dataKey="rx" name="Receive" type="monotone" stroke="var(--color-rx)" strokeWidth={2} dot={false} activeDot={{ r: 4 }} isAnimationActive={false} />
            <Line dataKey="tx" name="Send" type="monotone" stroke="var(--color-tx)" strokeWidth={2} strokeDasharray="4 3" dot={false} activeDot={{ r: 4 }} isAnimationActive={false} />
          </LineChart>
        </ChartContainer>
      </CardContent>
    </Card>
  )
}
