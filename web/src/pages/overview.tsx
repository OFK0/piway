import {
  CpuIcon,
  HardDrivesIcon,
  MemoryIcon,
  PlugsIcon,
  ThermometerIcon,
  WarningCircleIcon,
  WarningIcon,
} from '@phosphor-icons/react'
import { lazy, Suspense, useCallback, useState } from 'react'

import type { Snapshot } from '@/api'
import { AppShell } from '@/components/app-shell'
import { ConnectionStatus } from '@/components/connection-status'
import type { Range } from '@/components/live-charts'
import { MetricCard, MetricCardSkeleton } from '@/components/metric-card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useHealth } from '@/hooks/use-health'
import { useLive } from '@/hooks/use-live'
import { useNow } from '@/hooks/use-now'
import { bytes, bytesParts, percent, rate } from '@/format'
import { cpuStatus, hardwareAlerts, missingReason, temperatureStatus, usageStatus } from '@/lib/metrics'
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
  // Ages the "values from" note; runs only while disconnected.
  useNow(disconnected, 5000)

  return (
    <AppShell
      actions={<ConnectionStatus state={live} onRetry={live.retryNow} />}
      footer={health && <span>piway {health.version}</span>}
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
          className={cn('flex flex-col gap-8 transition-opacity', disconnected && 'opacity-60')}
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
        <section aria-label="Loading metrics" className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {[0, 1, 2, 3].map((i) => (
            <MetricCardSkeleton key={i} />
          ))}
        </section>
      )}
    </AppShell>
  )
}

function ChartsSkeleton() {
  return (
    <div aria-hidden="true" className="grid gap-4 lg:grid-cols-2">
      {[0, 1, 2, 3].map((i) => (
        <Skeleton key={i} className="h-64" />
      ))}
    </div>
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
  const memUsed = mem ? bytesParts(mem.used_bytes) : null

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
          value={s.cpu ? s.cpu.total.toFixed(1) : null}
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
          value={memUsed?.value ?? null}
          unit={memUsed?.unit}
          meter={memPercent}
          missingReason={missingReason(s, 'memory', false)}
          status={mem ? usageStatus(memPercent) : undefined}
        >
          {mem && <>of {bytes(mem.total_bytes)}, {bytes(mem.available_bytes)} available</>}
        </MetricCard>
        <MetricCard
          title="Temperature"
          icon={ThermometerIcon}
          value={temp !== null ? temp.toFixed(1) : null}
          unit="°C"
          missingReason={missingReason(s, 'thermal', false)}
          status={temp !== null ? temperatureStatus(temp) : undefined}
        >
          {temp !== null && 'Throttling starts at 80 °C'}
        </MetricCard>
        <MetricCard
          title="Storage"
          icon={HardDrivesIcon}
          value={root ? root.used_percent.toFixed(1) : null}
          unit="% used"
          meter={root?.used_percent}
          missingReason={missingReason(s, 'disk', false)}
          status={root ? usageStatus(root.used_percent) : undefined}
        >
          {root && <>{bytes(root.avail_bytes)} free of {bytes(root.total_bytes)}</>}
        </MetricCard>
      </section>

    </>
  )
}

function Tables({ s }: { s: Snapshot }) {
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <NetworkCard s={s} />
      <DisksCard s={s} />
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

function CoreMeters({ cores }: { cores: number[] }) {
  return (
    <ul className="grid grid-cols-4 gap-2" aria-label="Per-core usage">
      {cores.map((c, i) => (
        <li key={i} className="flex flex-col gap-1">
          <span className="h-1 w-full bg-muted" aria-hidden="true">
            <span className="block h-full bg-foreground" style={{ width: `${Math.min(c, 100)}%` }} />
          </span>
          <span className="tabular-nums">
            <span className="sr-only">Core {i + 1}: </span>
            {c.toFixed(0)}%
          </span>
        </li>
      ))}
    </ul>
  )
}

function NetworkCard({ s }: { s: Snapshot }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="type-title">Network</CardTitle>
        <CardDescription>Throughput since the previous sample. Errors include dropped packets.</CardDescription>
      </CardHeader>
      <CardContent>
        {s.network && s.network.length > 0 ? (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Interface</TableHead>
                <TableHead className="text-right">Receive</TableHead>
                <TableHead className="text-right">Send</TableHead>
                <TableHead className="text-right">Errors</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {s.network.map((n) => {
                const errs = n.rx_errors + n.tx_errors + n.rx_dropped + n.tx_dropped
                return (
                  <TableRow key={n.name}>
                    <TableCell className="type-code">{n.name}</TableCell>
                    <TableCell className="text-right tabular-nums">{rate(n.rx_bytes_per_sec)}</TableCell>
                    <TableCell className="text-right tabular-nums">{rate(n.tx_bytes_per_sec)}</TableCell>
                    <TableCell className="text-right tabular-nums">{errs === 0 ? '0' : errs}</TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        ) : (
          <p className="text-muted-foreground">{missingReason(s, 'network', true)}</p>
        )}
      </CardContent>
    </Card>
  )
}

function DisksCard({ s }: { s: Snapshot }) {
  const devices = s.disk?.devices
  return (
    <Card>
      <CardHeader>
        <CardTitle className="type-title">Disks</CardTitle>
        <CardDescription>I/O since the previous sample. Written is the total since boot.</CardDescription>
      </CardHeader>
      <CardContent>
        {devices && devices.length > 0 ? (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Device</TableHead>
                <TableHead className="text-right">Read</TableHead>
                <TableHead className="text-right">Write</TableHead>
                <TableHead className="text-right">Busy</TableHead>
                <TableHead className="text-right">Written</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {devices.map((d) => (
                <TableRow key={d.name}>
                  <TableCell className="type-code">{d.name}</TableCell>
                  <TableCell className="text-right tabular-nums">{rate(d.read_bytes_per_sec)}</TableCell>
                  <TableCell className="text-right tabular-nums">{rate(d.write_bytes_per_sec)}</TableCell>
                  <TableCell className="text-right tabular-nums">{percent(d.busy_percent)}</TableCell>
                  <TableCell className="text-right tabular-nums">{bytes(d.written_bytes)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : (
          <p className="text-muted-foreground">{missingReason(s, 'disk', true)}</p>
        )}
      </CardContent>
    </Card>
  )
}
