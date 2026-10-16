import { CheckCircleIcon, WarningCircleIcon, WarningIcon } from '@phosphor-icons/react'
import { useCallback } from 'react'

import { fetchAlerts, type Alert as AlertInfo } from '@/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useNow } from '@/hooks/use-now'
import { usePoll } from '@/hooks/use-poll'
import { ago, shortDuration } from '@/lib/time'

const docs = 'https://github.com/OFK0/piway/blob/main/docs/ALERTS.md'

function StateBadge({ a }: { a: AlertInfo }) {
  if (a.state === 'firing') {
    return (
      <Badge variant="destructive">
        <WarningCircleIcon data-icon="inline-start" weight="bold" />
        Firing
      </Badge>
    )
  }
  return (
    <Badge variant="outline">
      <WarningIcon data-icon="inline-start" />
      {a.state === 'pending' ? 'Pending' : 'Clearing'}
    </Badge>
  )
}

// What is wrong now, and the rules that decide it.
export function AlertsCard() {
  const load = useCallback((signal: AbortSignal) => fetchAlerts(signal), [])
  const poll = usePoll(load, 10_000)
  const now = useNow(true, 10_000)
  const data = poll.data

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="type-title">Alerts</h2>
        </CardTitle>
        <CardDescription>
          An alert fires when a rule holds for its whole “for” time and resolves after its “clear” time back to normal. Rules live in{' '}
          <code className="type-code">alerts.yaml</code> (
          <a href={docs} target="_blank" rel="noreferrer" className="underline underline-offset-4">
            ALERTS.md
          </a>
          ).
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        {poll.error && !data ? (
          <Alert variant="destructive">
            <WarningCircleIcon />
            <AlertTitle>Couldn't load alerts</AlertTitle>
            <AlertDescription>{poll.error instanceof Error ? poll.error.message : String(poll.error)}</AlertDescription>
          </Alert>
        ) : !data ? (
          <Skeleton aria-hidden className="h-32" />
        ) : (
          <>
            {data.active.length === 0 ? (
              <p className="flex items-center gap-2 text-muted-foreground">
                <CheckCircleIcon className="size-4" weight="fill" aria-hidden />
                Nothing is wrong right now.
              </p>
            ) : (
              <ul className="flex flex-col gap-3" aria-label="Active alerts">
                {data.active.map((a) => (
                  <li key={a.rule + a.series} className="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <StateBadge a={a} />
                    <span>{a.summary}</span>
                    <span className="type-caption text-muted-foreground">since {ago(a.since, now)}</span>
                  </li>
                ))}
              </ul>
            )}
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Rule</TableHead>
                    <TableHead className="hidden sm:table-cell">When</TableHead>
                    <TableHead className="hidden sm:table-cell">For</TableHead>
                    <TableHead className="hidden sm:table-cell">Clear</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.rules.map((r) => (
                    <TableRow key={r.name}>
                      <TableCell>
                        {r.name}
                        <span className="block type-code break-all whitespace-normal text-muted-foreground sm:hidden">
                          {r.metric} {r.condition}
                        </span>
                      </TableCell>
                      <TableCell className="hidden type-code text-muted-foreground sm:table-cell">
                        {r.metric} {r.condition}
                      </TableCell>
                      <TableCell className="hidden tabular-nums sm:table-cell">{shortDuration(r.for)}</TableCell>
                      <TableCell className="hidden tabular-nums sm:table-cell">{shortDuration(r.clear_for)}</TableCell>
                    </TableRow>
                  ))}
                  {data.deploy_failures && (
                    <TableRow>
                      <TableCell>Deploy failed</TableCell>
                      <TableCell className="hidden text-muted-foreground sm:table-cell">a deploy fails</TableCell>
                      <TableCell className="hidden sm:table-cell">at once</TableCell>
                      <TableCell className="hidden sm:table-cell">next deploy</TableCell>
                    </TableRow>
                  )}
                  {data.app_crashes && (
                    <TableRow>
                      <TableCell>App crashed</TableCell>
                      <TableCell className="hidden text-muted-foreground sm:table-cell">an app exits or runs out of memory</TableCell>
                      <TableCell className="hidden sm:table-cell">at once</TableCell>
                      <TableCell className="hidden sm:table-cell">10 min</TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
