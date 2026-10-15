import { CheckIcon, CopyIcon, KeyIcon, PlusIcon, WarningCircleIcon } from '@phosphor-icons/react'
import { useCallback, useState, type FormEvent } from 'react'
import { toast } from 'sonner'

import { AlertsCard } from '@/components/alerts-card'
import { AppShell } from '@/components/app-shell'
import { NotificationsCard } from '@/components/notifications-card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useNow } from '@/hooks/use-now'
import { usePoll } from '@/hooks/use-poll'
import { createToken, fetchTokens, revokeToken, type APIToken, type TokenCreated } from '@/lib/session'
import { ago } from '@/lib/time'

export default function SettingsPage() {
  const load = useCallback((signal: AbortSignal) => fetchTokens(signal), [])
  const poll = usePoll(load, 60_000)
  const [created, setCreated] = useState<TokenCreated | null>(null)

  return (
    <AppShell>
      <div className="flex flex-col gap-1">
        <h1 className="type-display">Settings</h1>
        <p className="text-muted-foreground">Alerts, notifications and access to this piway.</p>
      </div>

      <AlertsCard />
      <NotificationsCard />

      <Card>
        <CardHeader>
          <CardTitle>
            <h2 className="type-title">API tokens</h2>
          </CardTitle>
          <CardDescription>
            For scripts, Prometheus and the MCP server: send one as <code className="type-code">Authorization: Bearer …</code>. A
            token can do anything you can, except manage tokens.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-6">
          <CreateForm
            onCreated={(t) => {
              setCreated(t)
              poll.reload()
            }}
          />
          {created && <NewToken token={created} onDone={() => setCreated(null)} />}
          {poll.error && !poll.data ? (
            <Alert variant="destructive">
              <WarningCircleIcon />
              <AlertTitle>Couldn't load tokens</AlertTitle>
              <AlertDescription>{poll.error instanceof Error ? poll.error.message : String(poll.error)}</AlertDescription>
            </Alert>
          ) : !poll.data ? (
            <Skeleton aria-hidden className="h-24" />
          ) : poll.data.length === 0 ? (
            <Empty className="border">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <KeyIcon />
                </EmptyMedia>
                <EmptyTitle>No tokens yet</EmptyTitle>
                <EmptyDescription>Create one for each script or service, so you can revoke it alone.</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <TokenTable tokens={poll.data} onRevoked={poll.reload} />
          )}
        </CardContent>
      </Card>
    </AppShell>
  )
}

function CreateForm({ onCreated }: { onCreated: (t: TokenCreated) => void }) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!name.trim() || busy) return
    setBusy(true)
    try {
      onCreated(await createToken(name.trim()))
      setName('')
    } catch (err) {
      toast.error("Couldn't create the token", { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="flex flex-wrap items-end gap-3">
      <Field className="w-full max-w-xs">
        <FieldLabel htmlFor="token-name">Name</FieldLabel>
        <Input
          id="token-name"
          name="token-name"
          autoComplete="off"
          spellCheck={false}
          maxLength={64}
          placeholder="e.g. prometheus…"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
      <Button type="submit" disabled={busy || !name.trim()}>
        {busy ? <Spinner data-icon="inline-start" /> : <PlusIcon data-icon="inline-start" />}
        Create token
      </Button>
    </form>
  )
}

// The one time the token is shown. The clipboard needs a secure context
// (HTTPS or localhost), so the field stays selectable for the other cases.
function NewToken({ token, onDone }: { token: TokenCreated; onDone: () => void }) {
  const [copied, setCopied] = useState(false)
  const canCopy = typeof navigator.clipboard?.writeText === 'function'
  return (
    <Alert>
      <KeyIcon />
      <AlertTitle>Copy “{token.name}” now: it will not be shown again</AlertTitle>
      <AlertDescription className="flex flex-col gap-3">
        <Field>
          <FieldLabel htmlFor="new-token" className="sr-only">
            New token
          </FieldLabel>
          <div className="flex gap-2">
            <Input
              id="new-token"
              readOnly
              value={token.token}
              className="type-code"
              onFocus={(e) => e.currentTarget.select()}
              autoFocus
            />
            {canCopy && (
              <Button
                type="button"
                variant="outline"
                onClick={() =>
                  navigator.clipboard.writeText(token.token).then(
                    () => setCopied(true),
                    () => toast.error("Couldn't copy; select the token and copy it by hand"),
                  )
                }
              >
                {copied ? <CheckIcon data-icon="inline-start" /> : <CopyIcon data-icon="inline-start" />}
                {copied ? 'Copied' : 'Copy'}
              </Button>
            )}
          </div>
          <FieldDescription>piway keeps only a hash of it. If you lose it, revoke it and create another.</FieldDescription>
        </Field>
        <Button type="button" variant="outline" className="w-fit" onClick={onDone}>
          Done
        </Button>
      </AlertDescription>
    </Alert>
  )
}

function TokenTable({ tokens, onRevoked }: { tokens: APIToken[]; onRevoked: () => void }) {
  const now = useNow(true, 10_000)
  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead className="hidden sm:table-cell">Token</TableHead>
            <TableHead className="hidden sm:table-cell">Created</TableHead>
            <TableHead>Last used</TableHead>
            <TableHead>
              <span className="sr-only">Actions</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {tokens.map((t) => (
            <TableRow key={t.id}>
              <TableCell className="max-w-48 truncate">{t.name}</TableCell>
              <TableCell className="hidden type-code text-muted-foreground sm:table-cell">{t.hint}…</TableCell>
              <TableCell className="hidden text-muted-foreground sm:table-cell">{ago(t.created_at, now)}</TableCell>
              <TableCell className="text-muted-foreground">{t.last_used_at ? ago(t.last_used_at, now) : 'Never'}</TableCell>
              <TableCell className="text-right">
                <Revoke token={t} onRevoked={onRevoked} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function Revoke({ token, onRevoked }: { token: APIToken; onRevoked: () => void }) {
  const [busy, setBusy] = useState(false)
  async function run() {
    setBusy(true)
    try {
      await revokeToken(token.id)
      toast.success(`Revoked “${token.name}”`)
      onRevoked()
    } catch (err) {
      toast.error("Couldn't revoke the token", { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog>
      <DialogTrigger render={<Button variant="outline" size="sm" disabled={busy} />}>
        {busy && <Spinner data-icon="inline-start" />}
        Revoke
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Revoke “{token.name}”?</DialogTitle>
          <DialogDescription>Anything using it stops working at once. This cannot be undone.</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Keep it</DialogClose>
          <DialogClose render={<Button variant="destructive" onClick={run} />}>Revoke</DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
