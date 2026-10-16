import {
  ArrowSquareOutIcon,
  CheckCircleIcon,
  CopyIcon,
  GitBranchIcon,
  KeyIcon,
  LockIcon,
  PlusIcon,
  TrashIcon,
  WarningCircleIcon,
  WarningIcon,
} from '@phosphor-icons/react'
import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'

import {
  addRepo,
  fetchRepos,
  removeRepo,
  setRepoToken,
  testRepoAccess,
  type AccessResult,
  type Repo,
  type RepoAuth,
} from '@/api'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { usePoll } from '@/hooks/use-poll'
import { navigate } from '@/lib/navigation'
import { appPath } from '@/lib/routes'
import { parseGitLabRepo, parseRepo } from '@/lib/repo-input'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

const authLabel: Record<RepoAuth, string> = { 'deploy-key': 'Deploy key', token: 'Token' }

type Access = 'public' | RepoAuth
type Provider = 'github' | 'gitlab'

const providerOf = (r: Pick<Repo, 'provider'>): Provider => (r.provider === 'gitlab' ? 'gitlab' : 'github')
const providerName: Record<Provider, string> = { github: 'GitHub', gitlab: 'GitLab' }

// The repositories that deploy on push (apps.yaml), editable without a restart.
export function ReposCard({ onChange, onLoaded }: { onChange?: () => void; onLoaded?: () => void }) {
  const load = useCallback((signal: AbortSignal) => fetchRepos(signal), [])
  const repos = usePoll(load, 10_000)
  const data = repos.data
  const answered = data != null || repos.error != null
  useEffect(() => {
    if (answered) onLoaded?.()
  }, [answered, onLoaded])

  const changed = () => {
    repos.reload()
    onChange?.()
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Repositories</CardTitle>
        <CardDescription>A push to one of these branches on GitHub or GitLab deploys it.</CardDescription>
        <CardAction>
          <AddRepo
            onAdded={changed}
            webhookURLs={data ? { github: data.webhook.url ?? '', gitlab: (data.gitlab ?? data.webhook).url ?? '' } : null}
            privateAvailable={data?.private?.available ?? false}
            tokenSet={data?.private?.token_set ?? false}
          />
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {data && !data.webhook.configured && (
          <p className="flex items-start gap-2 type-caption text-muted-foreground">
            <WarningIcon className="mt-0.5 shrink-0" aria-hidden />
            <span>
              <code className="type-code">webhook_secret</code> is not set, so piway refuses every delivery from GitHub and GitLab.
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
              <RepoRow
                key={r.repo + '@' + r.branch + ':' + (r.path ?? '')}
                repo={r}
                tokenSet={data.private?.token_set ?? false}
                onChanged={changed}
              />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function RepoRow({ repo, tokenSet, onChanged }: { repo: Repo; tokenSet: boolean; onChanged: () => void }) {
  const [busy, setBusy] = useState(false)
  const label = `${repo.repo}@${repo.branch}${repo.path ? ':' + repo.path : ''}`
  const remove = async () => {
    setBusy(true)
    try {
      const res = await removeRepo(repo.repo, repo.branch, repo.path)
      const notes = [
        repo.app && `${repo.app} keeps running.`,
        res?.key_removed && `Its deploy key was deleted; remove it on ${providerName[providerOf(repo)]} too.`,
      ]
      toast.success(`Removed ${label}`, {
        description: notes.filter(Boolean).join(' ') || undefined,
      })
      onChanged()
    } catch (e) {
      toast.error("Couldn't remove the repository", { description: message(e) })
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="flex items-center gap-3 py-2">
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate type-title">{repo.repo}</span>
          {repo.provider === 'gitlab' && <Badge variant="secondary">{repo.host ? `GitLab · ${repo.host}` : 'GitLab'}</Badge>}
          {repo.auth && (
            <Badge variant="outline">
              <LockIcon data-icon="inline-start" aria-hidden />
              {authLabel[repo.auth]}
            </Badge>
          )}
        </span>
        <span className="flex items-center gap-1 type-caption text-muted-foreground">
          <GitBranchIcon aria-hidden /> {repo.branch}
          {repo.path && <span className="type-code">{' · ' + repo.path}</span>}
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
      {repo.auth && (
        <Dialog>
          <DialogTrigger render={<Button variant="ghost" size="icon" aria-label={`Access to ${repo.repo}`} />}>
            <KeyIcon />
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Access to {repo.repo}</DialogTitle>
              <DialogDescription>
                {repo.auth === 'deploy-key'
                  ? 'piway reads this private repository with its own deploy key.'
                  : `piway reads this private repository with the stored ${providerName[providerOf(repo)]} token.`}
              </DialogDescription>
            </DialogHeader>
            <AccessSteps repo={repo} tokenSet={tokenSet} onTokenSaved={onChanged} />
            <DialogFooter>
              <DialogClose render={<Button />}>Done</DialogClose>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
      <Dialog>
        <DialogTrigger
          render={<Button variant="ghost" size="icon" disabled={busy} aria-label={`Remove ${label}`} />}
        >
          {busy ? <Spinner aria-hidden /> : <TrashIcon />}
        </DialogTrigger>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Remove {label}?</DialogTitle>
            <DialogDescription>
              Pushes to this branch stop deploying. {repo.app ? `${repo.app} keeps running with its data, and you can` : 'You can'}{' '}
              add the repository again at any time.
              {repo.auth === 'deploy-key' &&
                " If it is the repository's last branch, its deploy key is deleted too; a new one is made if you add it again."}
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

function copy(text: string) {
  navigator.clipboard?.writeText(text).then(
    () => toast.success('Copied'),
    () => toast.error("Couldn't copy; select it instead"),
  )
}

// What a private repository needs on GitHub, and a way to check it.
function AccessSteps({ repo, tokenSet, onTokenSaved }: { repo: Repo; tokenSet: boolean; onTokenSaved?: () => void }) {
  const gitlab = repo.provider === 'gitlab'
  return (
    <div className="flex min-w-0 flex-col gap-4">
      {repo.auth === 'deploy-key' && repo.public_key && (
        <Field>
          <FieldLabel>Deploy key</FieldLabel>
          <div className="flex items-start gap-2">
            <code
              className="min-w-0 flex-1 border bg-muted px-2 py-1.5 type-code break-all select-all"
              aria-label="Public deploy key"
            >
              {repo.public_key}
            </code>
            <Button variant="outline" size="icon" aria-label="Copy the deploy key" onClick={() => copy(repo.public_key!)}>
              <CopyIcon />
            </Button>
          </div>
          <FieldDescription>
            {gitlab ? (
              <>
                On GitLab, add it under the project's Settings, Repository, Deploy keys, with any title. Leave{' '}
                <em>Grant write permissions to this key</em> off: piway only reads.
              </>
            ) : (
              <>
                On GitHub, add it under the repository's Settings, Deploy keys, with any title. Leave{' '}
                <em>Allow write access</em> off: piway only reads.
              </>
            )}
          </FieldDescription>
          {repo.deploy_key_url && (
            <Button
              variant="outline"
              size="sm"
              className="self-start"
              render={<a href={repo.deploy_key_url} target="_blank" rel="noreferrer" />}
              nativeButton={false}
            >
              <ArrowSquareOutIcon data-icon="inline-start" />
              {gitlab ? 'Open Deploy keys' : 'Open Add deploy key'}
            </Button>
          )}
        </Field>
      )}
      {repo.auth === 'token' && <TokenField repo={repo.repo} tokenSet={tokenSet} onSaved={onTokenSaved} />}
      <TestAccess repo={repo.repo} />
    </div>
  )
}

function TokenField({ repo, tokenSet, onSaved }: { repo: string; tokenSet: boolean; onSaved?: () => void }) {
  const [token, setToken] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(tokenSet)

  const save = async () => {
    setSaving(true)
    setError(null)
    try {
      await setRepoToken(token.trim())
      setToken('')
      setSaved(true)
      toast.success('Stored the token')
      onSaved?.()
    } catch (e) {
      setError(message(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Field data-invalid={error ? true : undefined}>
      <FieldLabel htmlFor="repo-token">{saved ? 'Replace the GitHub token' : 'GitHub token'}</FieldLabel>
      <div className="flex gap-2">
        <Input
          id="repo-token"
          type="password"
          autoComplete="off"
          spellCheck={false}
          placeholder={saved ? 'Stored; paste a new one to replace it' : 'github_pat_…'}
          value={token}
          onChange={(e) => setToken(e.target.value)}
        />
        <Button type="button" variant="outline" disabled={saving || token.trim() === ''} onClick={save}>
          {saving && <Spinner data-icon="inline-start" aria-hidden />}
          Save
        </Button>
      </div>
      {error && <FieldError>{error}</FieldError>}
      <FieldDescription>
        One fine-grained token serves every repository set to Token. Give it Contents: Read-only on {repo}, and nothing
        else. It is stored on the Pi and never shown again.
      </FieldDescription>
    </Field>
  )
}

function TestAccess({ repo }: { repo: string }) {
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState<AccessResult | null>(null)

  const run = async () => {
    setTesting(true)
    setResult(null)
    try {
      setResult(await testRepoAccess(repo))
    } catch (e) {
      setResult({ ok: false, message: message(e) })
    } finally {
      setTesting(false)
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-2" aria-live="polite">
      <Button type="button" variant="outline" size="sm" className="self-start" disabled={testing} onClick={run}>
        {testing && <Spinner data-icon="inline-start" aria-hidden />}
        Test access
      </Button>
      {result &&
        (result.ok ? (
          <Alert>
            <CheckCircleIcon aria-hidden />
            <AlertTitle>piway can read {repo}</AlertTitle>
            <AlertDescription>Pushes to it will deploy.</AlertDescription>
          </Alert>
        ) : (
          <Alert variant="destructive">
            <WarningCircleIcon aria-hidden />
            {/* Messages can carry a long URL; let it wrap inside the dialog. */}
            <AlertTitle className="[overflow-wrap:anywhere]">{result.message}</AlertTitle>
            {result.detail && (
              <AlertDescription>
                <code className="type-code [overflow-wrap:anywhere]">{result.detail}</code>
              </AlertDescription>
            )}
          </Alert>
        ))}
    </div>
  )
}

function AddRepo({
  onAdded,
  webhookURLs,
  privateAvailable,
  tokenSet,
}: {
  onAdded: () => void
  webhookURLs: Record<Provider, string> | null
  privateAvailable: boolean
  tokenSet: boolean
}) {
  const [open, setOpen] = useState(false)
  const [provider, setProvider] = useState<Provider>('github')
  const [host, setHost] = useState('')
  const [repo, setRepo] = useState('')
  const [branch, setBranch] = useState('main')
  const [dir, setDir] = useState('')
  const [access, setAccess] = useState<Access>('public')
  const [token, setToken] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [added, setAdded] = useState<Repo | null>(null)

  const reset = (next: boolean) => {
    setOpen(next)
    if (!next) {
      setProvider('github')
      setHost('')
      setRepo('')
      setBranch('main')
      setDir('')
      setAccess('public')
      setToken('')
      setError(null)
      setAdded(null)
    }
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const gitlab = provider === 'gitlab'
    const name = gitlab ? parseGitLabRepo(repo, host) : parseRepo(repo)
    if (!name) {
      setError(
        gitlab
          ? 'Use group/name or group/subgroup/name, or paste the GitLab URL.'
          : 'Use owner/name, for example OFK0/guestbook, or paste the GitHub URL.',
      )
      return
    }
    setSaving(true)
    setError(null)
    try {
      if (access === 'token' && token.trim() !== '') await setRepoToken(token.trim())
      const entry = await addRepo(
        name,
        branch.trim() || 'main',
        access === 'public' ? undefined : access,
        dir.trim().replace(/^\/+|\/+$/g, ''),
        gitlab ? { host: host.trim().replace(/^https?:\/\//i, '').replace(/\/+$/, '') } : undefined,
      )
      setAdded(entry)
      onAdded()
    } catch (err) {
      setError(message(err))
    } finally {
      setSaving(false)
    }
  }

  // webhookURL is the address the provider reaches piway at, as piway found
  // it; '' when it knows none (null: still loading). The address the dashboard was
  // opened at is not offered: it is often tailnet-only, which GitHub cannot
  // reach (#140).
  const addedProvider = added ? providerOf(added) : provider
  const pname = providerName[addedProvider]
  const webhookURL = webhookURLs ? webhookURLs[addedProvider] : null
  const webhook =
    webhookURL === '' ? (
      <Alert>
        <WarningIcon />
        <AlertTitle>piway does not know a public address</AlertTitle>
        <AlertDescription>
          {pname} must be able to reach piway to send pushes, and the address you opened the dashboard at may not be
          public. Publish the webhook (for example with Tailscale Funnel, see docs/REMOTE.md) and piway finds the
          address, or set <code className="type-code">webhook_url</code> in its config. Then add a webhook on the{' '}
          {addedProvider === 'gitlab' ? 'project' : 'repository'} with that address and the same secret as{' '}
          <code className="type-code">webhook_secret</code>
          {addedProvider === 'gitlab' ? ' as its Secret token, and the Push events trigger.' : ', content type application/json, and the push event.'}
        </AlertDescription>
      </Alert>
    ) : (
      webhookURL && (
    <Field>
      <FieldLabel>Payload URL</FieldLabel>
      <div className="flex gap-2">
        <Input readOnly value={webhookURL} className="type-code" onFocus={(e) => e.currentTarget.select()} />
        <Button variant="outline" size="icon" aria-label="Copy the payload URL" onClick={() => copy(webhookURL)}>
          <CopyIcon />
        </Button>
      </div>
      <FieldDescription>
        {addedProvider === 'gitlab' ? (
          <>
            Under Settings, Webhooks: the Secret token is piway's <code className="type-code">webhook_secret</code>, and
            the trigger is Push events.
          </>
        ) : (
          <>
            Content type <code className="type-code">application/json</code>, the same secret as piway's{' '}
            <code className="type-code">webhook_secret</code>, and the push event.
          </>
        )}{' '}
        This is the address {pname} reaches piway at, not the one you opened this dashboard at.
      </FieldDescription>
    </Field>
      )
    )

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
              <DialogTitle>
                {added.repo}@{added.branch}
                {added.path ? `:${added.path}` : ''} is listed
              </DialogTitle>
              <DialogDescription>
                {added.auth
                  ? `Two steps on ${pname}: let piway read the repository, then add a webhook so ${pname} tells piway about pushes.`
                  : `Last step: on the ${addedProvider === 'gitlab' ? "project's" : "repository's"} Settings, Webhooks, add a webhook so ${pname} tells piway about pushes.`}
              </DialogDescription>
            </DialogHeader>
            {added.auth ? (
              <div className="flex min-w-0 flex-col gap-6">
                <section className="flex flex-col gap-3" aria-labelledby="step-access">
                  <h3 id="step-access" className="type-title">
                    1. Access
                  </h3>
                  <AccessSteps repo={added} tokenSet={tokenSet || token.trim() !== ''} onTokenSaved={onAdded} />
                </section>
                {webhook && (
                  <section className="flex flex-col gap-3" aria-labelledby="step-webhook">
                    <h3 id="step-webhook" className="type-title">
                      2. Webhook
                    </h3>
                    {webhook}
                  </section>
                )}
              </div>
            ) : (
              webhook
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
              <Field>
                <FieldLabel id="repo-provider">Provider</FieldLabel>
                <ToggleGroup
                  variant="outline"
                  size="sm"
                  value={[provider]}
                  onValueChange={(v: string[]) => {
                    if (!v[0]) return
                    setProvider(v[0] as Provider)
                    // GitLab has no tokens here: deploy key or public only.
                    if (v[0] === 'gitlab' && access === 'token') setAccess('deploy-key')
                  }}
                  aria-labelledby="repo-provider"
                >
                  <ToggleGroupItem value="github">GitHub</ToggleGroupItem>
                  <ToggleGroupItem value="gitlab">GitLab</ToggleGroupItem>
                </ToggleGroup>
              </Field>
              {provider === 'gitlab' && (
                <Field>
                  <FieldLabel htmlFor="repo-host">GitLab server</FieldLabel>
                  <Input
                    id="repo-host"
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="gitlab.com"
                    value={host}
                    onChange={(e) => setHost(e.target.value)}
                  />
                  <FieldDescription>
                    Leave empty for gitlab.com. For a self-hosted server, its host name; add its SSH host key to{' '}
                    <code className="type-code">keys/known_hosts_extra</code> for a private project.
                  </FieldDescription>
                </Field>
              )}
              <Field data-invalid={error ? true : undefined}>
                <FieldLabel htmlFor="repo-name">{provider === 'gitlab' ? 'Project' : 'Repository'}</FieldLabel>
                <Input
                  id="repo-name"
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                  placeholder={provider === 'gitlab' ? 'group/name' : 'owner/name'}
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
              <Field>
                <FieldLabel htmlFor="repo-dir">App directory</FieldLabel>
                <Input
                  id="repo-dir"
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="the repository root"
                  value={dir}
                  onChange={(e) => setDir(e.target.value)}
                />
                <FieldDescription>
                  For a repository with several apps, such as <code className="type-code">apps/web</code>. Its piway.yaml
                  is read there.
                </FieldDescription>
              </Field>
              {privateAvailable && (
                <Field>
                  <FieldLabel id="repo-access">Access</FieldLabel>
                  <ToggleGroup
                    variant="outline"
                    size="sm"
                    value={[access]}
                    onValueChange={(v: string[]) => v[0] && setAccess(v[0] as Access)}
                    aria-labelledby="repo-access"
                  >
                    <ToggleGroupItem value="public">Public</ToggleGroupItem>
                    <ToggleGroupItem value="deploy-key">Deploy key</ToggleGroupItem>
                    {provider === 'github' && <ToggleGroupItem value="token">Token</ToggleGroupItem>}
                  </ToggleGroup>
                  <FieldDescription>
                    {access === 'public'
                      ? 'Anyone can read the repository; piway needs nothing.'
                      : access === 'deploy-key'
                        ? `Recommended for a private repository: piway makes a key that reads only this repository. You add it on ${providerName[provider]} next.`
                        : 'A fine-grained GitHub token with Contents: Read-only; one token can serve many repositories.'}
                  </FieldDescription>
                </Field>
              )}
              {access === 'token' && (
                <Field>
                  <FieldLabel htmlFor="repo-new-token">GitHub token</FieldLabel>
                  <Input
                    id="repo-new-token"
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    placeholder={tokenSet ? 'Stored; leave empty to keep it' : 'github_pat_…'}
                    value={token}
                    onChange={(e) => setToken(e.target.value)}
                  />
                  <FieldDescription>Stored on the Pi and never shown again.</FieldDescription>
                </Field>
              )}
            </FieldGroup>
            <DialogFooter>
              <DialogClose render={<Button type="button" variant="outline" />}>Cancel</DialogClose>
              <Button type="submit" disabled={saving || repo.trim() === '' || (access === 'token' && !tokenSet && token.trim() === '')}>
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
