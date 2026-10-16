import { ArrowsClockwiseIcon, EyeIcon, EyeSlashIcon, PencilSimpleIcon, PlusIcon, TrashIcon, WarningIcon } from '@phosphor-icons/react'
import { useCallback, useState } from 'react'
import { toast } from 'sonner'

import { fetchAppEnv, removeAppSecret, restartApp, setAppSecret, type AppEnv, type AppSecret, type InjectedVar } from '@/api'
import { Alert, AlertAction, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { usePoll } from '@/hooks/use-poll'
import { navigate } from '@/lib/navigation'
import { buildPath } from '@/lib/routes'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))
const validName = (s: string) => /^[A-Za-z_][A-Za-z0-9_]*$/.test(s)

// An app's environment: run.env from piway.yaml, and its secrets, which a
// signed-in browser can see and set. Changes apply on the next restart.
export function EnvCard({ app, busy }: { app: string; busy: boolean }) {
  const load = useCallback((signal: AbortSignal) => fetchAppEnv(app, signal), [app])
  const poll = usePoll(load, 15_000)
  const env = poll.data
  const [restarting, setRestarting] = useState(false)

  const restart = async () => {
    setRestarting(true)
    try {
      const b = await restartApp(app)
      toast.success('Restart queued', {
        description: 'A new container starts with the current secrets, then takes over.',
        action: { label: 'View log', onClick: () => void navigate(buildPath(b.id)) },
      })
      poll.reload()
    } catch (e) {
      toast.error("Couldn't restart", { description: message(e) })
    } finally {
      setRestarting(false)
    }
  }

  // Saved: offer the restart that applies it.
  const saved = (_: AppEnv, what: string) => {
    poll.reload()
    toast.success(what, {
      description: 'It applies when the app restarts.',
      action: { label: 'Restart now', onClick: () => void restart() },
    })
  }

  if (env?.static) return null
  if (poll.error && !env) {
    return (
      <Alert variant="destructive">
        <WarningIcon />
        <AlertTitle>Couldn't load the environment</AlertTitle>
        <AlertDescription>{message(poll.error)}</AlertDescription>
      </Alert>
    )
  }

  const empty = env && env.env.length === 0 && env.secrets.length === 0 && env.injected.length === 0
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          <h2 className="type-title">Environment</h2>
        </CardTitle>
        <CardDescription>
          Variables come from <code className="type-code">run.env</code> in piway.yaml. Secret values live on the Pi
          {env?.file ? (
            <>
              {' '}in <code className="type-code break-all">{env.file}</code>
            </>
          ) : null}
          , not in the repository.
        </CardDescription>
        {env?.values_shown && (
          <CardAction>
            <SecretDialog app={app} onSaved={(e) => saved(e, 'Secret saved')} />
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {env?.restart_needed && (
          <Alert>
            <ArrowsClockwiseIcon />
            <AlertTitle>Restart needed</AlertTitle>
            <AlertDescription>The running container was started with other secret values.</AlertDescription>
            <AlertAction>
              <Button size="sm" disabled={busy || restarting} onClick={restart}>
                {restarting && <Spinner data-icon="inline-start" aria-hidden />}
                Restart
              </Button>
            </AlertAction>
          </Alert>
        )}
        {env && !env.values_shown && env.secrets.length > 0 && (
          <p className="type-caption text-muted-foreground">Secret values are shown and set only in a signed-in browser.</p>
        )}
        {!env ? (
          <Skeleton aria-hidden className="h-24" />
        ) : empty ? (
          <p className="type-caption text-muted-foreground">
            No variables or secrets. Declare them under <code className="type-code">run</code> in piway.yaml.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Value</TableHead>
                  <TableHead className="w-0">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {env.env.map((v) => (
                  <TableRow key={'env:' + v.name}>
                    <TableCell className="type-code whitespace-normal [overflow-wrap:anywhere]">{v.name}</TableCell>
                    <TableCell className="whitespace-normal">
                      <code className="type-code [overflow-wrap:anywhere]">{v.value}</code>
                    </TableCell>
                    <TableCell className="type-caption text-muted-foreground">piway.yaml</TableCell>
                  </TableRow>
                ))}
                {env.secrets.map((s) => (
                  <SecretRow
                    key={'secret:' + s.name}
                    app={app}
                    secret={s}
                    editable={env.values_shown}
                    onSaved={saved}
                  />
                ))}
                {env.injected.map((v) => (
                  <InjectedRow key={'injected:' + v.name} variable={v} />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// A variable piway sets: read-only, its value (a credential) masked.
function InjectedRow({ variable: v }: { variable: InjectedVar }) {
  const [shown, setShown] = useState(false)
  return (
    <TableRow>
      <TableCell className="type-code whitespace-normal [overflow-wrap:anywhere]">{v.name}</TableCell>
      <TableCell className="whitespace-normal">
        {!v.set ? (
          <span className="type-caption text-muted-foreground">on next deploy</span>
        ) : v.value === undefined ? (
          <span className="type-caption text-muted-foreground">set</span>
        ) : (
          <span className="flex items-center gap-1">
            <code className="type-code [overflow-wrap:anywhere]">{shown ? v.value : '••••••••'}</code>
            <Button
              variant="ghost"
              size="icon"
              aria-label={shown ? `Hide ${v.name}` : `Show ${v.name}`}
              aria-pressed={shown}
              onClick={() => setShown((s) => !s)}
            >
              {shown ? <EyeSlashIcon /> : <EyeIcon />}
            </Button>
          </span>
        )}
      </TableCell>
      <TableCell className="type-caption text-muted-foreground">set by piway ({v.from})</TableCell>
    </TableRow>
  )
}

function SecretRow({
  app,
  secret,
  editable,
  onSaved,
}: {
  app: string
  secret: AppSecret
  editable: boolean
  onSaved: (env: AppEnv, what: string) => void
}) {
  const [shown, setShown] = useState(false)
  const [removing, setRemoving] = useState(false)

  const remove = async () => {
    setRemoving(true)
    try {
      onSaved(await removeAppSecret(app, secret.name), `Removed ${secret.name}`)
    } catch (e) {
      toast.error(`Couldn't remove ${secret.name}`, { description: message(e) })
    } finally {
      setRemoving(false)
    }
  }

  return (
    <TableRow>
      <TableCell className="type-code whitespace-normal [overflow-wrap:anywhere]">
        <span className="flex flex-wrap items-center gap-2">
          {secret.name}
          {!secret.declared && (
            <Badge variant="outline" title="Not in run.secrets, so the app does not get it">
              not declared
            </Badge>
          )}
        </span>
      </TableCell>
      <TableCell className="whitespace-normal">
        {!secret.set ? (
          <Badge variant="destructive">not set</Badge>
        ) : secret.value === undefined ? (
          <span className="type-caption text-muted-foreground">set</span>
        ) : (
          <span className="flex items-center gap-1">
            <code className="type-code [overflow-wrap:anywhere]">{shown ? secret.value : '••••••••'}</code>
            <Button
              variant="ghost"
              size="icon"
              aria-label={shown ? `Hide ${secret.name}` : `Show ${secret.name}`}
              aria-pressed={shown}
              onClick={() => setShown((v) => !v)}
            >
              {shown ? <EyeSlashIcon /> : <EyeIcon />}
            </Button>
          </span>
        )}
      </TableCell>
      <TableCell>
        {editable && (
          <span className="flex justify-end gap-1">
            <SecretDialog app={app} secret={secret} onSaved={(e) => onSaved(e, `Saved ${secret.name}`)} />
            {secret.set && (
              <Dialog>
                <DialogTrigger
                  render={<Button variant="ghost" size="icon" disabled={removing} aria-label={`Remove ${secret.name}`} />}
                >
                  {removing ? <Spinner aria-hidden /> : <TrashIcon />}
                </DialogTrigger>
                <DialogContent>
                  <DialogHeader>
                    <DialogTitle>Remove {secret.name}?</DialogTitle>
                    <DialogDescription>
                      {secret.declared
                        ? `The running app keeps its current value until it restarts. ${app} declares it, so its next deploy or restart fails until it has a value again.`
                        : 'The app does not use it: piway.yaml does not declare it.'}
                    </DialogDescription>
                  </DialogHeader>
                  <DialogFooter>
                    <DialogClose render={<Button variant="outline" />}>Keep it</DialogClose>
                    <DialogClose render={<Button variant="destructive" onClick={remove} />}>Remove</DialogClose>
                  </DialogFooter>
                </DialogContent>
              </Dialog>
            )}
          </span>
        )}
      </TableCell>
    </TableRow>
  )
}

// Sets one secret: an existing one (name fixed) or a new one.
function SecretDialog({ app, secret, onSaved }: { app: string; secret?: AppSecret; onSaved: (env: AppEnv) => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState(secret?.name ?? '')
  const [value, setValue] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  const reset = (next: boolean) => {
    setOpen(next)
    if (!next) {
      setName(secret?.name ?? '')
      setValue('')
      setError(null)
    }
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const key = name.trim()
    if (!validName(key)) {
      setError('Use letters, digits and _, not starting with a digit, such as API_KEY.')
      return
    }
    if (/[\r\n]/.test(value)) {
      setError('A value cannot span lines: the secrets file holds one NAME=value per line.')
      return
    }
    setSaving(true)
    setError(null)
    try {
      const env = await setAppSecret(app, key, value)
      reset(false)
      onSaved(env)
    } catch (err) {
      setError(message(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={reset}>
      {secret ? (
        <DialogTrigger
          render={<Button variant="ghost" size="icon" aria-label={secret.set ? `Change ${secret.name}` : `Set ${secret.name}`} />}
        >
          {secret.set ? <PencilSimpleIcon /> : <PlusIcon />}
        </DialogTrigger>
      ) : (
        <DialogTrigger render={<Button size="sm" variant="outline" />}>
          <PlusIcon data-icon="inline-start" />
          Add secret
        </DialogTrigger>
      )}
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{secret ? `${secret.set ? 'Change' : 'Set'} ${secret.name}` : 'Add a secret'}</DialogTitle>
            <DialogDescription>
              Stored on the Pi only. The app gets it on its next restart
              {secret?.declared === false || !secret ? ', if piway.yaml lists it under run.secrets' : ''}.
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            {!secret && (
              <Field>
                <FieldLabel htmlFor="secret-name">Name</FieldLabel>
                <Input
                  id="secret-name"
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="API_KEY"
                  className="type-code"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </Field>
            )}
            <Field data-invalid={error ? true : undefined}>
              <FieldLabel htmlFor="secret-value">Value</FieldLabel>
              <Input
                id="secret-value"
                autoFocus={!!secret}
                type="password"
                autoComplete="off"
                spellCheck={false}
                className="type-code"
                value={value}
                onChange={(e) => setValue(e.target.value)}
              />
              {error ? <FieldError>{error}</FieldError> : <FieldDescription>One line; it is not versioned.</FieldDescription>}
            </Field>
          </FieldGroup>
          <DialogFooter>
            <DialogClose render={<Button type="button" variant="outline" />}>Cancel</DialogClose>
            <Button type="submit" disabled={saving || name.trim() === ''}>
              {saving && <Spinner data-icon="inline-start" aria-hidden />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
