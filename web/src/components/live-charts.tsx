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
import { useHistory } from '@/hooks/use-history'
import type { Point } from '@/lib/history-points'

// Live charts for the overview (DESIGN.md › Data visualization): at most
// two series per chart, raspberry solid then ink dashed, one y-axis,
// thresholds as neutral dashed lines, values in text tokens.

export const ranges = { '1m': 60_000, '5m': 300_000, '24h': 86_400_000 } as const
export type Range = keyof typeof ranges


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
const minuteFormat = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit' })
const fmtMinute = (t: number) => minuteFormat.format(t)

const rangeWords: Record<Range, string> = { '1m': 'minute', '5m': '5 minutes', '24h': '24 hours' }

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
  return `${name} over the last ${rangeWords[range]}: now ${f(v[v.length - 1])}, low ${f(Math.min(...v))}, high ${f(Math.max(...v))}.`
}

export default function LiveCharts(props: { history: Snapshot[]; range: Range; onRangeChange: (r: Range) => void }) {
  const { history, range } = props
  // 24 hours come from the server's history (5-minute averages); the short
  // ranges from the snapshots this page has received.
  const longRange = range === '24h'
  const stored = useHistory(longRange)

  const points = useMemo(() => {
    if (longRange) return stored.points
    const all = history.map(toPoint)
    const end = all.length ? all[all.length - 1].t : 0
    return all.filter((p) => p.t >= end - ranges[range])
  }, [history, range, longRange, stored.points])
  const fmt = longRange ? fmtMinute : fmtTime
  // The 24-hour axis always spans 24 hours, so time piway was not running
  // shows as a gap instead of the chart stretching what data there is.
  const end = stored.at
  const xDomain: AxisDomain = longRange ? [end - ranges['24h'], end] : ['dataMin', 'dataMax']

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
          <ToggleGroupItem value="24h">24 h</ToggleGroupItem>
        </ToggleGroup>
      </div>
      {longRange && (stored.error || (stored.loading && points.length === 0)) && (
        <p role="status" className="text-muted-foreground">
          {stored.error ?? 'Loading the last 24 hours…'}
        </p>
      )}

      <div className="grid gap-4 lg:grid-cols-2">
        <ChartCard
          fmt={fmt}
          xDomain={xDomain}
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
          fmt={fmt}
          xDomain={xDomain}
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
          fmt={fmt}
          xDomain={xDomain}
          title="Memory"
          unit="%"
          current={now?.mem}
          label={summary('Memory used', '%', points.map((p) => p.mem), range)}
          config={{ v: { label: 'Used', color: 'var(--chart-1)' } }}
          data={points.map((p) => ({ t: p.t, v: p.mem }))}
          domain={[0, 100]}
        />
        <NetworkCard fmt={fmt} xDomain={xDomain} data={net.data} unit={net.unit} label={summary('Network receive', ` ${net.unit}`, net.data.map((p) => p.rx), range)} />
      </div>
    </section>
  )
}

const axis = { tickLine: false, axisLine: false, tickMargin: 8 } as const

type AxisDomain = [number, number] | ['dataMin', 'dataMax']

// Ticks every 4 hours on the local hour, for the 24-hour axis.
function hourTicks([lo, hi]: [number, number]): number[] {
  const step = 4 * 3_600_000
  const first = new Date(lo)
  first.setMinutes(0, 0, 0)
  first.setHours(Math.ceil(first.getHours() / 4) * 4)
  const out: number[] = []
  for (let t = first.getTime(); t <= hi; t += step) if (t >= lo) out.push(t)
  return out
}

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

function xAxis(fmt: (t: number) => string, domain: AxisDomain) {
  const ticks = typeof domain[0] === 'number' ? hourTicks(domain as [number, number]) : undefined
  return (
    <XAxis
      dataKey="t"
      type="number"
      scale="time"
      domain={domain}
      ticks={ticks}
      tickFormatter={fmt}
      minTickGap={48}
      {...axis}
    />
  )
}

function tooltip(unit: string, fmt: (t: number) => string) {
  return (
    <ChartTooltip
      cursor={{ stroke: 'var(--muted-foreground)' }}
      content={
        <ChartTooltipContent
          indicator="line"
          labelFormatter={(_, payload) => fmt(Number(payload?.[0]?.payload?.t))}
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
  fmt: (t: number) => string
  xDomain: AxisDomain
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
            {xAxis(props.fmt, props.xDomain)}
            <YAxis {...axis} width={36} domain={props.domain} ticks={evenTicks(props.domain)} allowDataOverflow />
            {props.threshold && (
              <ReferenceLine
                y={props.threshold.y}
                stroke="var(--chart-3)"
                strokeDasharray="4 3"
                label={{ value: props.threshold.label, position: 'insideTopRight', fill: 'var(--muted-foreground)', fontSize: 12 }}
              />
            )}
            {tooltip(props.unit, props.fmt)}
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

function NetworkCard(props: { fmt: (t: number) => string; xDomain: AxisDomain; data: { t: number; rx: number | null; tx: number | null }[]; unit: string; label: string }) {
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
            {xAxis(props.fmt, props.xDomain)}
            <YAxis {...axis} width={44} />
            {tooltip(props.unit, props.fmt)}
            <ChartLegend content={<ChartLegendContent />} />
            <Line dataKey="rx" name="Receive" type="monotone" stroke="var(--color-rx)" strokeWidth={2} dot={false} activeDot={{ r: 4 }} isAnimationActive={false} />
            <Line dataKey="tx" name="Send" type="monotone" stroke="var(--color-tx)" strokeWidth={2} strokeDasharray="4 3" dot={false} activeDot={{ r: 4 }} isAnimationActive={false} />
          </LineChart>
        </ChartContainer>
      </CardContent>
    </Card>
  )
}
