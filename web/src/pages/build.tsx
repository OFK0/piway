import { WarningCircleIcon, XIcon } from '@phosphor-icons/react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { buildLogStreamURL, cancelBuild, DeploysDisabledError, fetchBuild } from '@/api'
import { AppShell } from '@/components/app-shell'
import { BuildState } from '@/components/build-state'
import { DeploysOff } from '@/components/deploys-off'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { usePoll } from '@/hooks/use-poll'
import { appPath } from '@/lib/routes'
import { duration, shortSha } from '@/lib/time'

export default function BuildPage({ id }: { id: string }) {
  const load = useCallback((signal: AbortSignal) => fetchBuild(id, signal), [id])
  const poll = usePoll(load, 2000)
  const b = poll.data
  const active = b?.state === 'queued' || b?.state === 'running'

  const cancel = async () => {
    try {
      await cancelBuild(id)
      toast.success('Cancelling the build')
      poll.reload()
    } catch (e) {
      toast.error("Couldn't cancel", { description: e instanceof Error ? e.message : String(e) })
    }
  }

  return (
    <AppShell>
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="flex min-w-0 flex-col gap-1">
          {b?.app && (
            <a href={appPath(b.app)} className="w-fit type-caption text-muted-foreground underline-offset-4 hover:underline">
              {b.app}
            </a>
          )}
          <h1 className="type-display">{b?.kind === 'rollback' ? 'Rollback' : 'Build'}</h1>
          {b && (
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted-foreground">
              <BuildState build={b} />
              <span>
                {b.repo}@{b.branch} <code className="type-code text-foreground">{shortSha(b.commit)}</code>
              </span>
              {b.started_at && b.finished_at && <span>took {duration(b.started_at, b.finished_at)}</span>}
            </p>
          )}
        </div>
        {active && (
          <Button variant="destructive" onClick={cancel}>
            <XIcon data-icon="inline-start" />
            Cancel build
          </Button>
        )}
      </div>

      {poll.error instanceof DeploysDisabledError ? (
        <DeploysOff reason={poll.error.message} />
      ) : poll.error && !b ? (
        <Alert variant="destructive">
          <WarningCircleIcon />
          <AlertTitle>Couldn't load the build</AlertTitle>
          <AlertDescription>{String(poll.error)}</AlertDescription>
        </Alert>
      ) : !b ? (
        <Skeleton aria-hidden className="h-96" />
      ) : (
        <>
          {b.state === 'failed' && b.error && (
            <Alert variant="destructive">
              <WarningCircleIcon />
              <AlertTitle>The build failed</AlertTitle>
              <AlertDescription>{b.error}</AlertDescription>
            </Alert>
          )}
          {/* Re-key on state so a queued build's stream reconnects once it runs. */}
          <LogView key={b.state === 'queued' ? 'queued' : 'live'} id={id} live={active} />
        </>
      )}
    </AppShell>
  )
}

// The build's output, streamed: history first, then new chunks until the
// build ends. Follow keeps the newest line in view; scrolling up pauses it.
function LogView({ id, live }: { id: string; live: boolean }) {
  const [text, setText] = useState('')
  const [follow, setFollow] = useState(true)
  const [streamError, setStreamError] = useState<string | null>(null)
  const pre = useRef<HTMLPreElement>(null)

  useEffect(() => {
    const ws = new WebSocket(buildLogStreamURL(id))
    ws.onmessage = (e) => setText((t) => t + String(e.data))
    ws.onclose = (e) => {
      if (e.code !== 1000 && e.code !== 1005) setStreamError(`Log stream closed (${e.code}). Reload to see the rest.`)
    }
    return () => ws.close()
  }, [id])

  useEffect(() => {
    if (follow && pre.current) pre.current.scrollTop = pre.current.scrollHeight
  }, [text, follow])

  return (
    <section aria-labelledby="log-title" className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="log-title" className="type-title">
          Output
        </h2>
        <div className="flex items-center gap-2">
          <Switch id="follow" checked={follow} onCheckedChange={setFollow} />
          <Label htmlFor="follow">Follow output</Label>
        </div>
      </div>
      {streamError && <p className="text-destructive">{streamError}</p>}
      <pre
        ref={pre}
        tabIndex={0}
        aria-label="Build output"
        onWheel={(e) => e.deltaY < 0 && setFollow(false)}
        className="max-h-[70svh] min-h-64 overflow-auto border bg-muted p-4 type-code whitespace-pre-wrap break-all"
      >
        {text || (live ? 'Waiting for output…' : 'No output.')}
      </pre>
    </section>
  )
}
