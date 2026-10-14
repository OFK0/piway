import {
  CpuIcon,
  HardDrivesIcon,
  MemoryIcon,
  PlugsIcon,
  ThermometerIcon,
  WarningCircleIcon,
  WarningIcon,
} from '@phosphor-icons/react'
import { lazy, Suspense, useCallback, useEffect, useState, type ReactNode } from 'react'

import type { Snapshot } from '@/api'
import { AnimatedNumber } from '@/components/animated-number'
import { AppShell } from '@/components/app-shell'
import { ConnectionStatus } from '@/components/connection-status'
import type { Range } from '@/components/live-charts'
import { MetricCard, MetricCardSkeleton } from '@/components/metric-card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton, SkeletonText } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useHealth } from '@/hooks/use-health'
import { useLive } from '@/hooks/use-live'
import { useNow } from '@/hooks/use-now'
import { byteScale, bytes, count, percent, rate } from '@/format'
import { cpuStatus, hardwareAlerts, missingReason, temperatureStatus, usageStatus } from '@/lib/metrics'
import { loadShape, saveShape, type SnapshotShape } from '@/lib/snapshot-shape'
import { cn } from '@/lib/utils'

// Recharts is large; metric cards render first, charts follow.
const LiveCharts = lazy(() => import('@/components/live-charts'))

// The chart range lives in the URL (?range=1m) so a link shows the same view.
function useRange(): [Range, (r: Range) => void] {
  const [range, setRange] = useState<Range>(() =>
    new URLSearchParams(window.location.search).get('range') === '1m' ? '1m' : '5m',
  )
  const update = useCallback((r: Range) => {
    setRange(r)
    const url = new URL(window.location.href)
    if (r === '5m') url.searchParams.delete('range')
    else url.searchParams.set('range', r)
    window.history.replaceState(null, '', url)
  }, [])
  return [range, update]
}

export function OverviewPage() {
  const live = useLive()
  const health = useHealth()
  const [range, setRange] = useRange()
  const s = live.latest
  const disconnected = live.status !== 'live' && s !== null
  // Read once: the skeleton keeps the shape it was first drawn with.
  const [shape] = useState(loadShape)
  useEffect(() => {
    if (s) saveShape(s)
  }, [s])
  // Ages the "values from" note; runs only while disconnected.
  useNow(disconnected, 5000)

  return (
    <AppShell
      actions={<ConnectionStatus state={live} onRetry={live.retryNow} />}
      footer={health ? <span>piway {health.version}</span> : !s && <SkeletonText>piway v0.0.0</SkeletonText>}
    >
      <div className="flex flex-col gap-1">
        <h1 className="type-display">Overview</h1>
        <p className="text-muted-foreground">
          {disconnected && live.receivedAt ? (
            <>
              Disconnected. Showing values from{' '}
              <time dateTime={new Date(live.receivedAt).toISOString()}>
                {new Date(live.receivedAt).toLocaleTimeString()}
              </time>
              .
            </>
          ) : (
            'Live health of this Raspberry Pi.'
          )}
        </p>
      </div>

      {s ? (
        // While disconnected the last values stay readable but visibly stale.
        <div
          aria-busy={disconnected}
          className={cn(
            // Fades in once over the skeleton's place; later snapshots
            // update the values in place.
            'flex flex-col gap-8 transition-opacity animate-in duration-300 fade-in',
            disconnected && 'opacity-60',
          )}
        >
          <SnapshotView s={s} />
          <Suspense fallback={<ChartsSkeleton />}>
            <LiveCharts history={live.history} range={range} onRangeChange={setRange} />
          </Suspense>
          <Tables s={s} />
          <CollectorErrors s={s} />
        </div>
      ) : live.attempts >= 2 ? (
        <Unreachable retryAt={live.retryAt} onRetry={live.retryNow} />
      ) : (
        <SnapshotSkeleton shape={shape} />
      )}
    </AppShell>
  )
}

// Mirrors LiveCharts: the section title and range control, then the four
// chart cards with their real titles and a placeholder plot of the same height.
function ChartsSkeleton() {
  return (
    <section aria-hidden="true" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="type-title">Trends</h2>
        <SkeletonText className="h-8">1 min 5 min</SkeletonText>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        {['CPU', 'Temperature', 'Memory', 'Network'].map((title) => (
          <Card key={title}>
            <CardHeader>
              <CardTitle className="type-label">{title}</CardTitle>
              {title === 'Network' ? (
                <CardDescription>All interfaces</CardDescription>
              ) : (
                <CardAction>
                  <SkeletonText className="type-label">00.0 %</SkeletonText>
                </CardAction>
              )}
            </CardHeader>
            <CardContent>
              <Skeleton className="h-48" />
            </CardContent>
          </Card>
        ))}
      </div>
    </section>
  )
}

function Unreachable(props: { retryAt: number | null; onRetry: () => void }) {
  const now = useNow(true)
  const seconds = props.retryAt ? Math.max(0, Math.ceil((props.retryAt - now) / 1000)) : 0
  return (
    <Empty className="border">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <PlugsIcon />
        </EmptyMedia>
        <EmptyTitle>Can't reach piway</EmptyTitle>
        <EmptyDescription>
          Check that the agent is running.{' '}
          {seconds > 0 ? `Trying again in ${seconds}\u00a0s.` : 'Trying again…'}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button onClick={props.onRetry}>Try now</Button>
      </EmptyContent>
    </Empty>
  )
}

function SnapshotView({ s }: { s: Snapshot }) {
  const alerts = hardwareAlerts(s)
  const temp = s.thermal?.temp_celsius ?? null
  const root = s.disk?.filesystems.find((f) => f.path === '/')
  const mem = s.memory
  const memPercent = mem ? (mem.used_bytes / mem.total_bytes) * 100 : 0
  // Used memory in the unit of the total, so the unit does not flip while the
  // number animates across 1024.
  const memScale = mem ? byteScale(mem.total_bytes) : null

  return (
    <>
      {alerts.length > 0 && (
        <Alert variant="destructive">
          <WarningCircleIcon />
          <AlertTitle>The board is protecting itself</AlertTitle>
          <AlertDescription>
            <ul className="list-disc pl-4">
              {alerts.map((a) => (
                <li key={a}>{a}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      <section aria-label="Key metrics" className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <MetricCard
          title="CPU"
          icon={CpuIcon}
          value={s.cpu?.total ?? null}
          unit="%"
          meter={s.cpu?.total}
          missingReason={missingReason(s, 'cpu', true)}
          status={s.cpu ? cpuStatus(s.cpu.total) : undefined}
        >
          {s.cpu && <CoreMeters cores={s.cpu.per_core} />}
        </MetricCard>
        <MetricCard
          title="Memory"
          icon={MemoryIcon}
          value={mem && memScale ? mem.used_bytes / memScale.div : null}
          unit={memScale?.unit}
          meter={memPercent}
          missingReason={missingReason(s, 'memory', false)}
          status={mem ? usageStatus(memPercent) : undefined}
        >
          {mem && (
            <>
              of {bytes(mem.total_bytes)}, <AnimatedNumber value={mem.available_bytes} format={bytes} /> available
            </>
          )}
        </MetricCard>
        <MetricCard
          title="Temperature"
          icon={ThermometerIcon}
          value={temp}
          unit="°C"
          missingReason={missingReason(s, 'thermal', false)}
          status={temp !== null ? temperatureStatus(temp) : undefined}
        >
          {temp !== null && 'Throttling starts at 80 °C'}
        </MetricCard>
        <MetricCard
          title="Storage"
          icon={HardDrivesIcon}
          value={root?.used_percent ?? null}
          unit="% used"
          meter={root?.used_percent}
          missingReason={missingReason(s, 'disk', false)}
          status={root ? usageStatus(root.used_percent) : undefined}
        >
          {root && (
            <>
              <AnimatedNumber value={root.avail_bytes} format={bytes} /> free of {bytes(root.total_bytes)}
            </>
          )}
        </MetricCard>
      </section>

    </>
  )
}

function Tables({ s }: { s: Snapshot }) {
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <TableCard {...networkTable} rows={networkRows(s)} empty={missingReason(s, 'network', true)} />
      <TableCard {...disksTable} rows={diskRows(s)} empty={missingReason(s, 'disk', true)} />
    </div>
  )
}

function CollectorErrors({ s }: { s: Snapshot }) {
  if (!s.errors) return null
  return (
    <>
      {s.errors && (
        <Alert>
          <WarningIcon />
          <AlertTitle>Some metrics could not be collected</AlertTitle>
          <AlertDescription>
            <ul className="flex flex-col gap-1">
              {Object.entries(s.errors).map(([name, msg]) => (
                <li key={name}>
                  <span className="font-medium text-foreground capitalize">{name}</span>:{' '}
                  <code className="type-code">{msg}</code>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
    </>
  )
}

const wholePercent = (n: number) => `${n.toFixed(0)}%`

function CoreMeters({ cores }: { cores: number[] }) {
  return (
    <ul className="grid grid-cols-4 gap-2" aria-label="Per-core usage">
      {cores.map((c, i) => (
        <li key={i} className="flex flex-col gap-1">
          <span className="h-1 w-full bg-muted" aria-hidden="true">
            <span
              className="block h-full bg-foreground transition-[width] duration-600 ease-out"
              style={{ width: `${Math.min(c, 100)}%` }}
            />
          </span>
          <span>
            <span className="sr-only">Core {i + 1}: </span>
            <AnimatedNumber value={c} format={wholePercent} />
          </span>
        </li>
      ))}
    </ul>
  )
}

// The page as it will look, with placeholders where the values go. Row counts
// come from the last load (see snapshot-shape.ts), so the height matches too.
function SnapshotSkeleton({ shape }: { shape: SnapshotShape }) {
  return (
    <div className="flex flex-col gap-8" aria-busy="true">
      <p role="status" className="sr-only">
        Loading metrics
      </p>
      <section aria-hidden="true" className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <MetricCardSkeleton title="CPU" icon={CpuIcon} meter>
          {shape.cores > 0 && <CoreMetersSkeleton count={shape.cores} />}
        </MetricCardSkeleton>
        <MetricCardSkeleton title="Memory" icon={MemoryIcon} meter />
        <MetricCardSkeleton title="Temperature" icon={ThermometerIcon} />
        <MetricCardSkeleton title="Storage" icon={HardDrivesIcon} meter />
      </section>
      <ChartsSkeleton />
      <div aria-hidden="true" className="grid gap-4 lg:grid-cols-2">
        <TableCard {...networkTable} rows={null} loadingRows={shape.interfaces} />
        <TableCard {...disksTable} rows={null} loadingRows={shape.devices} />
      </div>
    </div>
  )
}

function CoreMetersSkeleton({ count }: { count: number }) {
  return (
    <ul className="grid grid-cols-4 gap-2">
      {Array.from({ length: count }, (_, i) => (
        <li key={i} className="flex flex-col gap-1">
          <span className="h-1 w-full bg-muted" />
          <SkeletonText className="w-fit">00%</SkeletonText>
        </li>
      ))}
    </ul>
  )
}

interface TableSpec {
  title: string
  description: string
  /** The first column is the identifier; the others are right-aligned numbers. */
  columns: { label: string; placeholder: string }[]
}

const networkTable: TableSpec = {
  title: 'Network',
  description: 'Throughput since the previous sample. Errors include dropped packets.',
  columns: [
    { label: 'Interface', placeholder: 'wlan0' },
    { label: 'Receive', placeholder: '0.0 KiB/s' },
    { label: 'Send', placeholder: '0.0 KiB/s' },
    { label: 'Errors', placeholder: '0' },
  ],
}

const disksTable: TableSpec = {
  title: 'Disks',
  description: 'I/O since the previous sample. Written is the total since boot.',
  columns: [
    { label: 'Device', placeholder: 'mmcblk0' },
    { label: 'Read', placeholder: '0 B/s' },
    { label: 'Write', placeholder: '0 B/s' },
    { label: 'Busy', placeholder: '0.0%' },
    { label: 'Written', placeholder: '0.0 GiB' },
  ],
}

interface Row {
  key: string
  cells: ReactNode[]
}

function networkRows(s: Snapshot): Row[] {
  return (s.network ?? []).map((n) => {
    const errs = n.rx_errors + n.tx_errors + n.rx_dropped + n.tx_dropped
    return {
      key: n.name,
      cells: [
        n.name,
        <AnimatedNumber key="rx" value={n.rx_bytes_per_sec} format={rate} />,
        <AnimatedNumber key="tx" value={n.tx_bytes_per_sec} format={rate} />,
        <AnimatedNumber key="errors" value={errs} format={count} />,
      ],
    }
  })
}

function diskRows(s: Snapshot): Row[] {
  return (s.disk?.devices ?? []).map((d) => ({
    key: d.name,
    cells: [
      d.name,
      <AnimatedNumber key="read" value={d.read_bytes_per_sec} format={rate} />,
      <AnimatedNumber key="write" value={d.write_bytes_per_sec} format={rate} />,
      <AnimatedNumber key="busy" value={d.busy_percent} format={percent} />,
      <AnimatedNumber key="written" value={d.written_bytes} format={bytes} />,
    ],
  }))
}

// A card with a table of rows. rows === null means loading: the header and
// column names are real, and loadingRows rows of placeholders stand in for
// the data.
function TableCard(props: TableSpec & { rows: Row[] | null; loadingRows?: number; empty?: string }) {
  const rows: Row[] =
    props.rows ??
    Array.from({ length: props.loadingRows ?? 0 }, (_, i) => ({
      key: String(i),
      cells: props.columns.map((c) => <SkeletonText key={c.label}>{c.placeholder}</SkeletonText>),
    }))

  return (
    <Card>
      <CardHeader>
        <CardTitle className="type-title">{props.title}</CardTitle>
        <CardDescription>{props.description}</CardDescription>
      </CardHeader>
      <CardContent>
        {rows.length > 0 ? (
          <Table>
            <TableHeader>
              <TableRow>
                {props.columns.map((c, i) => (
                  <TableHead key={c.label} className={i > 0 ? 'text-right' : undefined}>
                    {c.label}
                  </TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((r) => (
                <TableRow key={r.key}>
                  {r.cells.map((cell, i) => (
                    <TableCell key={i} className={i === 0 ? 'type-code' : 'text-right tabular-nums'}>
                      {cell}
                    </TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : props.rows === null ? (
          <SkeletonText>No interfaces reported.</SkeletonText>
        ) : (
          <p className="text-muted-foreground">{props.empty}</p>
        )}
      </CardContent>
    </Card>
  )
}
