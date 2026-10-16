import { TrashIcon, WarningIcon } from '@phosphor-icons/react'
import { useCallback, useState } from 'react'
import { toast } from 'sonner'

import { fetchAppVolumes, removeAppVolume, type AppVolume } from '@/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { usePoll } from '@/hooks/use-poll'
import { bytes } from '@/format'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

// An app's volumes (run.volumes) and the space they take. Nothing shows
// for an app without volumes.
export function VolumesCard({ app }: { app: string }) {
  const load = useCallback((signal: AbortSignal) => fetchAppVolumes(app, signal), [app])
  const poll = usePoll(load, 30_000)
  const volumes = poll.data

  if (poll.error && !volumes) {
    return (
      <Alert variant="destructive">
        <WarningIcon />
        <AlertTitle>Couldn't load the volumes</AlertTitle>
        <AlertDescription>{message(poll.error)}</AlertDescription>
      </Alert>
    )
  }
  if (!volumes || volumes.length === 0) return null

  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="type-title">Volumes</h2>
        </CardTitle>
        <CardDescription>
          Data here survives deploys, restarts and rollbacks. During a deploy the old and the new container share it.
        </CardDescription>
      </CardHeader>
      <CardContent className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Volume</TableHead>
              <TableHead>Mounted at</TableHead>
              <TableHead className="text-right">Size</TableHead>
              <TableHead className="w-0">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {volumes.map((v) => (
              <VolumeRow key={v.name} app={app} volume={v} onRemoved={poll.reload} />
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}

function VolumeRow({ app, volume: v, onRemoved }: { app: string; volume: AppVolume; onRemoved: () => void }) {
  const [busy, setBusy] = useState(false)
  const remove = async () => {
    setBusy(true)
    try {
      await removeAppVolume(app, v.name)
      toast.success(`Deleted the volume ${v.name}`)
      onRemoved()
    } catch (e) {
      toast.error(`Couldn't delete ${v.name}`, { description: message(e) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <TableRow>
      <TableCell className="whitespace-normal">
        <span className="flex flex-wrap items-center gap-2">
          <span className="type-code">{v.name}</span>
          {!v.declared && <Badge variant="outline">not declared</Badge>}
        </span>
      </TableCell>
      <TableCell className="whitespace-normal">
        {v.path ? (
          <code className="type-code [overflow-wrap:anywhere]">{v.path}</code>
        ) : (
          <span className="type-caption text-muted-foreground">not mounted</span>
        )}
      </TableCell>
      <TableCell className="text-right tabular-nums">
        {!v.created ? (
          <span className="type-caption text-muted-foreground">on next deploy</span>
        ) : v.size_bytes < 0 ? (
          '—'
        ) : (
          bytes(v.size_bytes)
        )}
      </TableCell>
      <TableCell>
        {!v.declared && v.created && (
          <Dialog>
            <DialogTrigger
              render={<Button variant="ghost" size="icon" disabled={busy || v.in_use} aria-label={`Delete the volume ${v.name}`} />}
            >
              {busy ? <Spinner aria-hidden /> : <TrashIcon />}
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Delete the volume {v.name}?</DialogTitle>
                <DialogDescription>
                  {app}'s piway.yaml no longer lists it. Deleting it removes its data
                  {v.size_bytes > 0 ? ` (${bytes(v.size_bytes)})` : ''} for good; it cannot be undone.
                </DialogDescription>
              </DialogHeader>
              <DialogFooter>
                <DialogClose render={<Button variant="outline" />}>Keep it</DialogClose>
                <DialogClose render={<Button variant="destructive" onClick={remove} />}>Delete</DialogClose>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        )}
      </TableCell>
    </TableRow>
  )
}
