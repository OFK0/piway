import { ArrowUpIcon, DatabaseIcon, WarningCircleIcon } from '@phosphor-icons/react'
import { useCallback, useState } from 'react'
import { toast } from 'sonner'

import { backupDatabases, DeploysDisabledError, fetchDatabases, type DatabaseEngine } from '@/api'
import { AppShell } from '@/components/app-shell'
import { DeploysOff } from '@/components/deploys-off'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { bytes } from '@/format'
import { useNow } from '@/hooks/use-now'
import { usePoll } from '@/hooks/use-poll'
import { navigate } from '@/lib/navigation'
import { appPath } from '@/lib/routes'
import { ago } from '@/lib/time'

const titles: Record<DatabaseEngine['name'], string> = { postgres: 'PostgreSQL', mariadb: 'MariaDB', redis: 'Redis' }

// The shared database engines (docs/DATABASES.md): state, size, memory
// and the apps with a database on each.
export default function DatabasesPage() {
  const load = useCallback((signal: AbortSignal) => fetchDatabases(signal), [])
  const poll = usePoll(load, 10_000)
  const now = useNow(true, 30_000)
  const [backingUp, setBackingUp] = useState(false)
  const engines = poll.data
  const anyApps = engines?.some((e) => e.apps.length > 0) ?? false

  const backup = async () => {
    setBackingUp(true)
    try {
      const r = await backupDatabases()
      toast.success(r.done.length === 0 ? 'Nothing to back up' : `Backed up ${r.done.length} databases`)
      poll.reload()
    } catch (e) {
      toast.error("Couldn't back up every database", { description: e instanceof Error ? e.message : String(e) })
      poll.reload()
    } finally {
      setBackingUp(false)
    }
  }

  return (
    <AppShell>
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex flex-col gap-1">
          <h1 className="type-display">Databases</h1>
          <p className="text-muted-foreground">
            One shared server per engine; each app gets its own database. Backed up every night at 03:30.
          </p>
        </div>
        {anyApps && (
          <Button variant="outline" disabled={backingUp} onClick={backup}>
            {backingUp && <Spinner data-icon="inline-start" aria-hidden />}
            Back up now
          </Button>
        )}
      </div>

      {poll.error instanceof DeploysDisabledError ? (
        <DeploysOff reason={poll.error.message} />
      ) : poll.error && !engines ? (
        <Alert variant="destructive">
          <WarningCircleIcon />
          <AlertTitle>Couldn't load the databases</AlertTitle>
          <AlertDescription>{String(poll.error)}</AlertDescription>
        </Alert>
      ) : !engines ? (
        <Skeleton aria-hidden className="h-64" />
      ) : (
        <>
          {!engines.some((e) => e.state !== 'not_started') && (
            <Empty className="border">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <DatabaseIcon />
                </EmptyMedia>
                <EmptyTitle>No database runs yet</EmptyTitle>
                <EmptyDescription>
                  An engine starts the first time an app asks for it: add{' '}
                  <code className="type-code">databases: [postgres]</code> under <code className="type-code">run</code> in
                  its piway.yaml and deploy.
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          )}
          <div className="grid items-start gap-4 lg:grid-cols-3">
            {engines.map((e) => (
              <EngineCard key={e.name} engine={e} now={now} />
            ))}
          </div>
        </>
      )}
    </AppShell>
  )
}

function stateBadge(state: string) {
  if (state === 'running') return <Badge variant="secondary">Running</Badge>
  if (state === 'not_started') return <Badge variant="outline">Not started</Badge>
  return <Badge variant="destructive">{state.charAt(0).toUpperCase() + state.slice(1)}</Badge>
}

function EngineCard({ engine: e, now }: { engine: DatabaseEngine; now: number }) {
  const started = e.state !== 'not_started'
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="type-title">{titles[e.name]}</h2>
        </CardTitle>
        <CardDescription>
          <code className="type-code">{e.image ?? e.pinned}</code>
        </CardDescription>
        <CardAction>{stateBadge(e.state)}</CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {started && (
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1">
            <dt className="type-caption text-muted-foreground">Data</dt>
            <dd className="text-right tabular-nums">{e.size_bytes >= 0 ? bytes(e.size_bytes) : '—'}</dd>
            <dt className="type-caption text-muted-foreground">Memory</dt>
            <dd className="text-right tabular-nums">
              {e.memory_bytes >= 0 ? `${bytes(e.memory_bytes)} of ${bytes(e.memory_limit_bytes)}` : '—'}
            </dd>
          </dl>
        )}
        {e.upgrade_available && (
          <Alert>
            <ArrowUpIcon />
            <AlertTitle>{e.pinned} is available</AlertTitle>
            <AlertDescription>
              This piway starts new servers on {e.pinned}. Move this one with{' '}
              <code className="type-code">piway db upgrade {e.name}</code> (dump and restore; the apps see errors meanwhile).
            </AlertDescription>
          </Alert>
        )}
        {e.apps.length === 0 ? (
          <p className="type-caption text-muted-foreground">
            {started ? 'No app has a database here.' : 'Starts when an app asks for it.'}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>App</TableHead>
                <TableHead className="text-right">Last backup</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {e.apps.map((a) => (
                <TableRow key={a.name}>
                  <TableCell>
                    <a
                      href={appPath(a.name)}
                      onClick={(ev) => {
                        ev.preventDefault()
                        void navigate(appPath(a.name))
                      }}
                      className="underline-offset-4 hover:underline"
                    >
                      {a.name}
                    </a>
                  </TableCell>
                  <TableCell className="text-right text-muted-foreground">
                    {a.last_backup ? (
                      <span title={`${a.backups} kept`}>
                        {ago(a.last_backup, now)}
                        {a.last_backup_bytes !== undefined && `, ${bytes(a.last_backup_bytes)}`}
                      </span>
                    ) : (
                      'none yet'
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
