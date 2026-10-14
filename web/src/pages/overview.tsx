import {
  ArrowClockwiseIcon,
  CpuIcon,
  HardDrivesIcon,
  MemoryIcon,
  PlugsIcon,
  ThermometerIcon,
  WarningCircleIcon,
  WarningIcon,
} from '@phosphor-icons/react'

import type { Snapshot } from '@/api'
import { AppShell } from '@/components/app-shell'
import { MetricCard, MetricCardSkeleton } from '@/components/metric-card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useSnapshot } from '@/hooks/use-snapshot'
import { bytes, bytesParts, percent, rate } from '@/format'
import { cpuStatus, hardwareAlerts, missingReason, temperatureStatus, usageStatus } from '@/lib/metrics'

export function OverviewPage() {
  const { snapshot, health, error, loading, receivedAt, refresh } = useSnapshot()

  return (
    <AppShell footer={health && <span>piway {health.version}</span>}>
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="type-display">Overview</h1>
          <p className="text-muted-foreground">
            {receivedAt ? (
              <>
                Updated at <time dateTime={receivedAt.toISOString()}>{receivedAt.toLocaleTimeString()}</time>
              </>
            ) : (
              'Health of this Raspberry Pi.'
            )}
          </p>
        </div>
        <Button variant="outline" onClick={refresh} disabled={loading}>
          {loading ? <Spinner data-icon="inline-start" /> : <ArrowClockwiseIcon data-icon="inline-start" />}
          Refresh
        </Button>
      </div>

      {error && snapshot && (
        <Alert variant="destructive">
          <WarningCircleIcon />
          <AlertTitle>Couldn't refresh</AlertTitle>
          <AlertDescription>{error}. Showing the last values received.</AlertDescription>
        </Alert>
      )}

      {error && !snapshot ? (
        <Unreachable error={error} onRetry={refresh} retrying={loading} />
      ) : snapshot ? (
        <SnapshotView s={snapshot} />
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

function Unreachable(props: { error: string; onRetry: () => void; retrying: boolean }) {
  return (
    <Empty className="border">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <PlugsIcon />
        </EmptyMedia>
        <EmptyTitle>Can't reach piway</EmptyTitle>
        <EmptyDescription>
          {props.error}. Check that the agent is running, then try again.
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button onClick={props.onRetry} disabled={props.retrying}>
          {props.retrying && <Spinner data-icon="inline-start" />}
          Try again
        </Button>
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

      <div className="grid gap-4 lg:grid-cols-2">
        <NetworkCard s={s} />
        <DisksCard s={s} />
      </div>

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
        <CardDescription>Throughput since the previous sample.</CardDescription>
      </CardHeader>
      <CardContent>
        {s.network && s.network.length > 0 ? (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Interface</TableHead>
                <TableHead className="text-right">Receive</TableHead>
                <TableHead className="text-right">Send</TableHead>
                <TableHead className="text-right">Errors and drops</TableHead>
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
