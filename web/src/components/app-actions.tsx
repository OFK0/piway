import { ArrowCounterClockwiseIcon, ArrowsClockwiseIcon } from '@phosphor-icons/react'
import { useState } from 'react'
import { toast } from 'sonner'

import { redeployApp, rollbackApp, type AppInfo, type Build } from '@/api'
import { Button } from '@/components/ui/button'
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
import { buildPath } from '@/lib/routes'
import { shortSha } from '@/lib/time'

function queued(what: string, b: Build) {
  toast.success(`${what} queued`, {
    description: `${b.app ?? b.repo} at ${shortSha(b.commit)}`,
    action: { label: 'View log', onClick: () => window.location.assign(buildPath(b.id)) },
  })
}

function failed(what: string, e: unknown) {
  toast.error(`Couldn't ${what}`, { description: e instanceof Error ? e.message : String(e) })
}

// Redeploy and Rollback for one app. busy disables both while a build of
// the app is queued or running.
export function AppActions(props: { app: AppInfo; busy: boolean; onDone: () => void; size?: 'sm' | 'default' }) {
  const { app, busy } = props
  const [pending, setPending] = useState<'redeploy' | 'rollback' | null>(null)

  const run = async (kind: 'redeploy' | 'rollback') => {
    setPending(kind)
    try {
      const b = kind === 'redeploy' ? await redeployApp(app.name) : await rollbackApp(app.name)
      queued(kind === 'redeploy' ? 'Redeploy' : 'Rollback', b)
      props.onDone()
    } catch (e) {
      failed(kind === 'redeploy' ? 'redeploy' : 'roll back', e)
    } finally {
      setPending(null)
    }
  }

  const disabled = busy || pending !== null || !app.live
  return (
    <div className="flex flex-wrap gap-2">
      <Button variant="outline" size={props.size} disabled={disabled} onClick={() => run('redeploy')}>
        {pending === 'redeploy' ? <Spinner data-icon="inline-start" aria-hidden /> : <ArrowsClockwiseIcon data-icon="inline-start" />}
        Redeploy
      </Button>
      <Dialog>
        <DialogTrigger render={<Button variant="outline" size={props.size} disabled={disabled} />}>
          {pending === 'rollback' ? <Spinner data-icon="inline-start" aria-hidden /> : <ArrowCounterClockwiseIcon data-icon="inline-start" />}
          Roll back
        </DialogTrigger>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Roll back {app.name}?</DialogTitle>
            <DialogDescription>
              piway re-releases the image {app.name} ran before the current one, without rebuilding. It goes through the
              health check first: if it fails, the current version keeps serving.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Keep current version</DialogClose>
            <DialogClose render={<Button onClick={() => run('rollback')} />}>Roll back</DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
