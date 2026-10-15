import { CopyIcon, GitBranchIcon, PlusIcon, TrashIcon, WarningIcon } from '@phosphor-icons/react'
import { useCallback, useState } from 'react'
import { toast } from 'sonner'

import { addRepo, fetchRepos, removeRepo, type Repo } from '@/api'
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
import { usePoll } from '@/hooks/use-poll'
import { navigate } from '@/lib/navigation'
import { appPath } from '@/lib/routes'
import { parseRepo } from '@/lib/repo-input'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

// The repositories that deploy on push (apps.yaml), editable without a restart.
export function ReposCard({ onChange }: { onChange?: () => void }) {
  const load = useCallback((signal: AbortSignal) => fetchRepos(signal), [])
  const repos = usePoll(load, 10_000)
  const data = repos.data

  const changed = () => {
    repos.reload()
    onChange?.()
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Repositories</CardTitle>
        <CardDescription>A push to one of these branches on GitHub deploys it.</CardDescription>
        <CardAction>
          <AddRepo onAdded={changed} webhookURL={data ? location.origin + data.webhook.path : null} />
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {data && !data.webhook.configured && (
          <p className="flex items-start gap-2 type-caption text-muted-foreground">
            <WarningIcon className="mt-0.5 shrink-0" aria-hidden />
            <span>
              <code className="type-code">webhook_secret</code> is not set, so piway refuses every delivery from GitHub.
              Set it, then restart piway once.
            </span>
          </p>
        )}
        {data === null ? (
          <Skeleton className="h-10 w-full" />
        ) : data.repos.length === 0 ? (
          <p className="type-caption text-muted-foreground">No repositories yet. Add one to start deploying.</p>
        ) : (
          <ul className="flex flex-col divide-y" aria-label="Repositories">
            {data.repos.map((r) => (
              <RepoRow key={r.repo + '@' + r.branch} repo={r} onRemoved={changed} />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function RepoRow({ repo, onRemoved }: { repo: Repo; onRemoved: () => void }) {
  const [busy, setBusy] = useState(false)
  const remove = async () => {
    setBusy(true)
    try {
      await removeRepo(repo.repo, repo.branch)
      toast.success(`Removed ${repo.repo}@${repo.branch}`, {
        description: repo.app ? `${repo.app} keeps running.` : undefined,
      })
      onRemoved()
    } catch (e) {
      toast.error("Couldn't remove the repository", { description: message(e) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="flex items-center gap-3 py-2">
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="truncate type-title">{repo.repo}</span>
        <span className="flex items-center gap-1 type-caption text-muted-foreground">
          <GitBranchIcon aria-hidden /> {repo.branch}
          {repo.app && (
            <>
              {' · '}
              <a href={appPath(repo.app)} onClick={(e) => { e.preventDefault(); void navigate(appPath(repo.app!)) }} className="underline underline-offset-4 hover:text-foreground">
                {repo.app}
              </a>
            </>
          )}
        </span>
      </div>
      <Dialog>
        <DialogTrigger
          render={<Button variant="ghost" size="icon" disabled={busy} aria-label={`Remove ${repo.repo}@${repo.branch}`} />}
        >
          {busy ? <Spinner aria-hidden /> : <TrashIcon />}
        </DialogTrigger>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove {repo.repo}@{repo.branch}?</DialogTitle>
            <DialogDescription>
              Pushes to this branch stop deploying. {repo.app ? `${repo.app} keeps running with its data, and you can` : 'You can'}{' '}
              add the repository again at any time.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose render={<Button variant="outline" />}>Keep it</DialogClose>
            <DialogClose render={<Button variant="destructive" onClick={remove} />}>Remove</DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </li>
  )
}

function AddRepo({ onAdded, webhookURL }: { onAdded: () => void; webhookURL: string | null }) {
  const [open, setOpen] = useState(false)
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [added, setAdded] = useState<string | null>(null)

  const reset = (next: boolean) => {
    setOpen(next)
    if (!next) {
      setRepo('')
      setBranch('main')
      setError(null)
      setAdded(null)
    }
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const name = parseRepo(repo)
    if (!name) {
      setError('Use owner/name, for example OFK0/guestbook, or paste the GitHub URL.')
      return
    }
    setSaving(true)
    setError(null)
    try {
      await addRepo(name, branch.trim() || 'main')
      setAdded(`${name}@${branch.trim() || 'main'}`)
      onAdded()
    } catch (err) {
      setError(message(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={reset}>
      <DialogTrigger render={<Button size="sm" />}>
        <PlusIcon data-icon="inline-start" />
        Add repository
      </DialogTrigger>
      <DialogContent>
        {added ? (
          <>
            <DialogHeader>
              <DialogTitle>{added} is listed</DialogTitle>
              <DialogDescription>
                Last step: on the repository's Settings, Webhooks, add a webhook so GitHub tells piway about pushes.
              </DialogDescription>
            </DialogHeader>
            {webhookURL && (
              <Field>
                <FieldLabel>Payload URL</FieldLabel>
                <div className="flex gap-2">
                  <Input readOnly value={webhookURL} className="type-code" onFocus={(e) => e.currentTarget.select()} />
                  <Button
                    variant="outline"
                    size="icon"
                    aria-label="Copy the payload URL"
                    onClick={() =>
                      navigator.clipboard?.writeText(webhookURL).then(
                        () => toast.success('Copied'),
                        () => toast.error("Couldn't copy; select it instead"),
                      )
                    }
                  >
                    <CopyIcon />
                  </Button>
                </div>
                <FieldDescription>
                  Content type <code className="type-code">application/json</code>, the same secret as piway's{' '}
                  <code className="type-code">webhook_secret</code>, and the push event. The address must be reachable from
                  GitHub; <code className="type-code">localhost</code> is not.
                </FieldDescription>
              </Field>
            )}
            <DialogFooter>
              <DialogClose render={<Button />}>Done</DialogClose>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>Add a repository</DialogTitle>
              <DialogDescription>
                piway builds the repository's Dockerfile on each push to the branch and serves it as an app.
              </DialogDescription>
            </DialogHeader>
            <FieldGroup>
              <Field data-invalid={error ? true : undefined}>
                <FieldLabel htmlFor="repo-name">Repository</FieldLabel>
                <Input
                  id="repo-name"
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="owner/name"
                  value={repo}
                  onChange={(e) => setRepo(e.target.value)}
                />
                {error && <FieldError>{error}</FieldError>}
              </Field>
              <Field>
                <FieldLabel htmlFor="repo-branch">Branch</FieldLabel>
                <Input
                  id="repo-branch"
                  autoComplete="off"
                  spellCheck={false}
                  value={branch}
                  onChange={(e) => setBranch(e.target.value)}
                />
              </Field>
            </FieldGroup>
            <DialogFooter>
              <DialogClose render={<Button type="button" variant="outline" />}>Cancel</DialogClose>
              <Button type="submit" disabled={saving || repo.trim() === ''}>
                {saving && <Spinner data-icon="inline-start" aria-hidden />}
                Add
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
