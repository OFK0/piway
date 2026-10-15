import { WarningCircleIcon } from '@phosphor-icons/react'
import { useCallback } from 'react'

import { DeploysDisabledError, fetchApps, type AppInfo } from '@/api'
import { AppActions } from '@/components/app-actions'
import { AppAddress } from '@/components/app-address'
import { AppShell } from '@/components/app-shell'
import { AppStatus, isBusy } from '@/components/app-status'
import { DeploysOff, NoApps } from '@/components/deploys-off'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { useNow } from '@/hooks/use-now'
import { usePoll } from '@/hooks/use-poll'
import { appPath, buildPath } from '@/lib/routes'
import { ago, shortSha } from '@/lib/time'

export default function AppsPage() {
  const load = useCallback((signal: AbortSignal) => fetchApps(signal), [])
  const apps = usePoll(load, 5000)
  const now = useNow(true, 10_000)

  return (
    <AppShell>
      <div className="flex flex-col gap-1">
        <h1 className="type-display">Apps</h1>
        <p className="text-muted-foreground">Deployed from git push, one build at a time.</p>
      </div>
      {apps.error instanceof DeploysDisabledError ? (
        <DeploysOff reason={apps.error.message} />
      ) : apps.error && !apps.data ? (
        <Alert variant="destructive">
          <WarningCircleIcon />
          <AlertTitle>Couldn't load apps</AlertTitle>
          <AlertDescription>{String(apps.error)}</AlertDescription>
        </Alert>
      ) : apps.data === null ? (
        <AppListSkeleton />
      ) : apps.data.length === 0 ? (
        <NoApps />
      ) : (
        <ul className="flex flex-col gap-3" aria-label="Apps">
          {apps.data.map((a) => (
            <AppRow key={a.name} app={a} now={now} onChange={apps.reload} />
          ))}
        </ul>
      )}
    </AppShell>
  )
}

function AppRow({ app, now, onChange }: { app: AppInfo; now: number; onChange: () => void }) {
  return (
    <li>
      <Card>
        <CardContent className="grid gap-4 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,2fr)_auto] md:items-center">
          <div className="flex min-w-0 flex-col gap-0.5">
            <a href={appPath(app.name)} className="w-fit type-title underline-offset-4 hover:underline">
              {app.name}
            </a>
            <span className="truncate type-caption text-muted-foreground">
              {app.repo}@{app.branch}
            </span>
          </div>
          <div>
            <AppStatus app={app} />
          </div>
          <div className="flex min-w-0 flex-col gap-0.5 type-caption text-muted-foreground">
            {app.live ? (
              <span>
                <code className="type-code text-foreground">{shortSha(app.live.commit)}</code>{' '}
                {app.live.kind === 'rollback' ? 'rolled back' : 'deployed'} {ago(app.live.finished_at ?? app.live.queued_at, now)}
              </span>
            ) : (
              <span>No successful deploy yet</span>
            )}
            {app.last && app.last.id !== app.live?.id && (
              <a href={buildPath(app.last.id)} className="w-fit underline underline-offset-4 hover:text-foreground">
                Latest build: {app.last.state}
              </a>
            )}
            <AppAddress app={app} />
          </div>
          <AppActions app={app} busy={isBusy(app)} onDone={onChange} size="sm" />
        </CardContent>
      </Card>
    </li>
  )
}

function AppListSkeleton() {
  return (
    <div aria-busy="true" className="flex flex-col gap-3">
      <p role="status" className="sr-only">
        Loading apps
      </p>
      {[0, 1].map((i) => (
        <Skeleton key={i} className="h-24" />
      ))}
    </div>
  )
}
