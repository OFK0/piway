import { LockKeyIcon, WarningCircleIcon } from '@phosphor-icons/react'
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'

import { Logo } from '@/components/logo'
import { ThemeToggle } from '@/components/theme-toggle'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { login, LoginError, type Session } from '@/lib/session'

// A centered card with the brand, outside the app shell: nothing behind
// sign-in is shown, not even the navigation.
function Frame({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-svh flex-col">
      <header className="mx-auto flex h-14 w-full max-w-7xl items-center justify-between px-4 md:px-6">
        <Logo />
        <ThemeToggle />
      </header>
      <main className="flex flex-1 items-start justify-center px-4 pt-[12vh] pb-12">
        <Card className="w-full max-w-sm">{children}</Card>
      </main>
    </div>
  )
}

export function LoginPage(props: { onSignedIn: (s: Session) => void; unreachable?: string; onRetry?: () => void }) {
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [waitUntil, setWaitUntil] = useState(0)
  const [now, setNow] = useState(() => Date.now())
  const input = useRef<HTMLInputElement>(null)

  const locked = waitUntil > now
  useEffect(() => {
    if (!locked) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [locked])
  // The lockout is over: its message no longer applies.
  if (waitUntil && !locked) {
    setWaitUntil(0)
    setError(null)
  }

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!password || busy || locked) return
    setBusy(true)
    setError(null)
    try {
      props.onSignedIn(await login(password))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      if (err instanceof LoginError && err.retryAfter) {
        const t = Date.now()
        setNow(t)
        setWaitUntil(t + err.retryAfter * 1000)
      }
      setPassword('')
      setBusy(false)
      requestAnimationFrame(() => input.current?.focus())
    }
  }

  const seconds = Math.ceil((waitUntil - now) / 1000)
  return (
    <Frame>
      <CardHeader>
        <CardTitle>
          <h1 className="type-title">Sign in</h1>
        </CardTitle>
        <CardDescription>This piway is private. Enter its password to continue.</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {props.unreachable && (
          <Alert variant="destructive">
            <WarningCircleIcon />
            <AlertTitle>piway is not answering</AlertTitle>
            <AlertDescription>
              {props.unreachable}{' '}
              <button type="button" className="underline underline-offset-4" onClick={props.onRetry}>
                Try again
              </button>
            </AlertDescription>
          </Alert>
        )}
        <form onSubmit={submit} noValidate>
          <FieldGroup>
            <Field data-invalid={error ? true : undefined}>
              <FieldLabel htmlFor="password">Password</FieldLabel>
              <Input
                ref={input}
                id="password"
                name="password"
                type="password"
                autoComplete="current-password"
                autoFocus
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? 'password-error' : undefined}
              />
              {error && (
                <FieldError id="password-error" role="alert">
                  {locked ? `Too many wrong passwords. Try again in ${seconds} s.` : error.replace(/^./, (c) => c.toUpperCase()) + '.'}
                </FieldError>
              )}
            </Field>
            <Button type="submit" disabled={busy || locked || !password}>
              {busy ? <Spinner data-icon="inline-start" /> : <LockKeyIcon data-icon="inline-start" />}
              Sign in
            </Button>
          </FieldGroup>
        </form>
      </CardContent>
    </Frame>
  )
}

// No password is set and this browser is not on the Pi itself.
export function LocalOnlyPage() {
  return (
    <Frame>
      <CardHeader>
        <CardTitle>
          <h1 className="type-title">Set a password first</h1>
        </CardTitle>
        <CardDescription>
          Until piway has a password, it only answers the Pi itself. Set one on the Pi, then reload this page:
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <pre className="overflow-x-auto border bg-muted px-3 py-2 type-code">piway -set-password</pre>
        <p className="type-caption text-muted-foreground">
          Or reach it through an SSH tunnel: <code className="type-code">ssh -L 8080:localhost:8080 &lt;user&gt;@&lt;pi&gt;</code>, then open
          localhost:8080.
        </p>
      </CardContent>
    </Frame>
  )
}
