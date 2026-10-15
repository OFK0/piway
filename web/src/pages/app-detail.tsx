import { WarningCircleIcon } from '@phosphor-icons/react'
import { useCallback } from 'react'

import { DeploysDisabledError, fetchApps, fetchBuilds, type AppInfo, type Build } from '@/api'
import { AppActions } from '@/components/app-actions'
import { AppAddress } from '@/components/app-address'
import { AppShell } from '@/components/app-shell'
import { AppStatus, isBusy } from '@/components/app-status'
import { BuildState } from '@/components/build-state'
import { DeploysOff } from '@/components/deploys-off'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useNow } from '@/hooks/use-now'
import { usePoll } from '@/hooks/use-poll'
import { buildPath } from '@/lib/routes'
import { ago, duration, shortSha } from '@/lib/time'
import { buildCause, capitalize, doneVerb } from '@/lib/build-labels'

export default function AppDetailPage({ name }: { name: string }) {
  const load = useCallback(
    async (signal: AbortSignal) => {
      const [apps, builds] = await Promise.all([fetchApps(signal), fetchBuilds(signal)])
      return { app: apps.find((a) => a.name === name) ?? null, builds: builds.filter((b) => b.app === name) }
    },
    [name],
  )
  const poll = usePoll(load, 3000)
  const now = useNow(true, 10_000)
  const app = poll.data?.app

  return (
    <AppShell>
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-1">
          <a href="/apps" className="w-fit type-caption text-muted-foreground underline-offset-4 hover:underline">
            Apps
          </a>
          <h1 className="type-display">{name}</h1>
          {app && (
            <p className="text-muted-foreground">
              {app.repo}@{app.branch}, served at <AppAddress app={app} />
            </p>
          )}
        </div>
        {app && <AppActions app={app} busy={isBusy(app)} onDone={poll.reload} />}
      </div>

      {poll.error instanceof DeploysDisabledError ? (
        <DeploysOff reason={poll.error.message} />
      ) : poll.error && !poll.data ? (
        <Alert variant="destructive">
          <WarningCircleIcon />
          <AlertTitle>Couldn't load {name}</AlertTitle>
          <AlertDescription>{String(poll.error)}</AlertDescription>
        </Alert>
      ) : !poll.data ? (
        <Skeleton aria-hidden className="h-64" />
      ) : !app ? (
        <Alert>
          <WarningCircleIcon />
          <AlertTitle>No app named {name}</AlertTitle>
          <AlertDescription>
            It has never been built here. <a href="/apps" className="underline underline-offset-4">See all apps</a>.
          </AlertDescription>
        </Alert>
      ) : (
        <>
          <LiveCard app={app} now={now} />
          <History builds={poll.data.builds} now={now} />
        </>
      )}
    </AppShell>
  )
}

function LiveCard({ app, now }: { app: AppInfo; now: number }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="type-title">Now running</h2>
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-x-8 gap-y-3">
        <AppStatus app={app} />
        {app.live ? (
          <>
            <span>
              Commit <code className="type-code">{shortSha(app.live.commit)}</code>
            </span>
            <span className="text-muted-foreground">
              {capitalize(doneVerb(app.live))}{' '}
              {ago(app.live.finished_at ?? app.live.queued_at, now)}
            </span>
            {app.live.site ? (
              <span className="text-muted-foreground">Static site</span>
            ) : (
              app.container?.image && <code className="type-code text-muted-foreground">{app.container.image}</code>
            )}
          </>
        ) : (
          <span className="text-muted-foreground">No successful deploy yet.</span>
        )}
      </CardContent>
    </Card>
  )
}

function History({ builds, now }: { builds: Build[]; now: number }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="type-title">History</h2>
        </CardTitle>
        <CardDescription>The last builds and rollbacks of this app, newest first.</CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Build</TableHead>
              <TableHead>Commit</TableHead>
              <TableHead>State</TableHead>
              <TableHead className="text-right">Took</TableHead>
              <TableHead className="text-right">When</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {builds.map((b) => (
              <TableRow key={b.id}>
                <TableCell>
                  <a href={buildPath(b.id)} className="underline-offset-4 hover:underline">
                    {buildCause(b)}
                  </a>
                </TableCell>
                <TableCell className="type-code">{shortSha(b.commit)}</TableCell>
                <TableCell>
                  <BuildState build={b} />
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {b.started_at && b.finished_at ? duration(b.started_at, b.finished_at) : '—'}
                </TableCell>
                <TableCell className="text-right text-muted-foreground">{ago(b.queued_at, now)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}
