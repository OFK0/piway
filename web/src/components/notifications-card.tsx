import { BellRingingIcon, BellSlashIcon, PaperPlaneTiltIcon, WarningCircleIcon } from '@phosphor-icons/react'
import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useNow } from '@/hooks/use-now'
import { usePoll } from '@/hooks/use-poll'
import {
  currentSubscription,
  deviceId,
  disablePush,
  enablePush,
  fetchPushStatus,
  PermissionError,
  removeDevice,
  sendTest,
  unsupportedReason,
  type PushDevice,
} from '@/lib/push'
import { ago } from '@/lib/time'

const docs = 'https://github.com/OFK0/piway/blob/main/docs/'

const reasons: Record<string, { title: string; body: React.ReactNode }> = {
  insecure: {
    title: 'Needs HTTPS',
    body: (
      <>
        Browsers only allow notifications on HTTPS pages (or localhost). Open piway through Tailscale (
        <a href={docs + 'REMOTE.md'} target="_blank" rel="noreferrer" className="underline underline-offset-4">
          REMOTE.md
        </a>
        ) and turn them on there.
      </>
    ),
  },
  'ios-home-screen': {
    title: 'Add piway to the Home Screen first',
    body: 'On iPhone and iPad, only web apps on the Home Screen get notifications: tap Share, then Add to Home Screen, open piway from there and come back here.',
  },
  unsupported: { title: 'This browser cannot get notifications', body: 'Try a current Chrome, Edge, Firefox or Safari.' },
}

// Subscribes this browser to alerts and lists every subscribed device.
export function NotificationsCard() {
  const load = useCallback((signal: AbortSignal) => fetchPushStatus(signal), [])
  const poll = usePoll(load, 30_000)
  const [thisId, setThisId] = useState<string | null | undefined>(undefined) // undefined: not checked yet
  const [busy, setBusy] = useState<'on' | 'off' | 'test' | null>(null)
  const unsupported = unsupportedReason()

  useEffect(() => {
    if (unsupported) return
    currentSubscription()
      .then((s) => (s ? deviceId(s.endpoint) : null))
      .then(setThisId)
      .catch(() => setThisId(null))
  }, [unsupported])

  const devices = poll.data?.subscriptions ?? []
  const subscribedHere = thisId != null && devices.some((d) => d.id === thisId)

  async function turnOn() {
    if (!poll.data) return
    setBusy('on')
    try {
      const d = await enablePush(poll.data.public_key)
      setThisId(d.id)
      poll.reload()
      toast.success('Notifications are on for this device', {
        action: { label: 'Send a test', onClick: () => void test(d.id) },
      })
    } catch (err) {
      toast.error("Couldn't turn notifications on", {
        description:
          err instanceof PermissionError && err.permission === 'denied'
            ? 'Notifications are blocked for this site: allow them in the browser’s site settings, then try again.'
            : err instanceof Error
              ? err.message
              : String(err),
      })
    } finally {
      setBusy(null)
    }
  }

  async function turnOff() {
    setBusy('off')
    try {
      await disablePush()
      setThisId(null)
      poll.reload()
      toast.success('Notifications are off for this device')
    } catch (err) {
      toast.error("Couldn't turn notifications off", { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(null)
    }
  }

  async function test(id?: string) {
    setBusy('test')
    try {
      const results = await sendTest(id)
      const failed = results.filter((r) => !r.ok)
      if (results.length === 0) toast.info('No devices to notify yet')
      else if (failed.length === 0) toast.success(`Test sent to ${results.length === 1 ? results[0].label : `${results.length} devices`}`)
      else toast.error(`Couldn't reach ${failed.map((r) => r.label).join(', ')}`, { description: failed[0].error })
      poll.reload()
    } catch (err) {
      toast.error("Couldn't send the test", { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(null)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="type-title">Notifications</h2>
        </CardTitle>
        <CardDescription>
          Alerts arrive on the devices you turn this on for, even with the dashboard closed. They go through the browser’s own push
          service, encrypted end to end: no account needed.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        {unsupported ? (
          <Alert>
            <WarningCircleIcon />
            <AlertTitle>{reasons[unsupported].title}</AlertTitle>
            <AlertDescription>{reasons[unsupported].body}</AlertDescription>
          </Alert>
        ) : (
          <div className="flex flex-wrap items-center gap-3">
            {thisId === undefined || !poll.data ? (
              <Skeleton aria-hidden className="h-9 w-56" />
            ) : subscribedHere ? (
              <>
                <Badge variant="secondary">
                  <BellRingingIcon data-icon="inline-start" />
                  On for this device
                </Badge>
                <Button variant="outline" onClick={() => void test(thisId ?? undefined)} disabled={busy !== null}>
                  {busy === 'test' ? <Spinner data-icon="inline-start" /> : <PaperPlaneTiltIcon data-icon="inline-start" />}
                  Send a test
                </Button>
                <Button variant="outline" onClick={() => void turnOff()} disabled={busy !== null}>
                  {busy === 'off' ? <Spinner data-icon="inline-start" /> : <BellSlashIcon data-icon="inline-start" />}
                  Turn off here
                </Button>
              </>
            ) : (
              <Button onClick={() => void turnOn()} disabled={busy !== null}>
                {busy === 'on' ? <Spinner data-icon="inline-start" /> : <BellRingingIcon data-icon="inline-start" />}
                Notify this device
              </Button>
            )}
          </div>
        )}
        {poll.error && !poll.data ? (
          <Alert variant="destructive">
            <WarningCircleIcon />
            <AlertTitle>Couldn't load devices</AlertTitle>
            <AlertDescription>{poll.error instanceof Error ? poll.error.message : String(poll.error)}</AlertDescription>
          </Alert>
        ) : (
          devices.length > 0 && <Devices devices={devices} thisId={thisId ?? null} onChange={poll.reload} />
        )}
      </CardContent>
    </Card>
  )
}

function Devices({ devices, thisId, onChange }: { devices: PushDevice[]; thisId: string | null; onChange: () => void }) {
  const now = useNow(true, 10_000)
  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Device</TableHead>
            <TableHead className="hidden sm:table-cell">Last delivery</TableHead>
            <TableHead>
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {devices.map((d) => (
            <TableRow key={d.id}>
              <TableCell>
                <div className="flex flex-col gap-0.5">
                  <span>
                    {d.label}
                    {d.id === thisId && <span className="text-muted-foreground"> (this one)</span>}
                  </span>
                  {d.last_error && <span className="type-caption text-destructive">{d.last_error}</span>}
                </div>
              </TableCell>
              <TableCell className="hidden text-muted-foreground sm:table-cell">
                {d.last_ok_at ? ago(d.last_ok_at, now) : 'Not yet'}
              </TableCell>
              <TableCell className="text-right">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    removeDevice(d.id).then(
                      () => {
                        toast.success(`Removed ${d.label}`)
                        onChange()
                      },
                      (err) => toast.error("Couldn't remove it", { description: String(err) }),
                    )
                  }
                >
                  Remove
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
