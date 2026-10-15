import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'

import { AppShell } from '@/components/app-shell'
import { fetchSession, logout, onSignedOut, type Session } from '@/lib/session'
import { LocalOnlyPage, LoginPage } from '@/pages/login'

interface SessionValue {
  session: Session
  signOut: () => Promise<void>
}

const SessionContext = createContext<SessionValue | null>(null)

/** The signed-in session, or null outside a SessionGate (the design system). */
export const useSession = () => useContext(SessionContext)

// Shows the sign-in page until the server accepts this browser, and again
// as soon as any API call says the session is gone.
export function SessionGate({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(() => {
    fetchSession()
      .then((s) => {
        setSession(s)
        setError(null)
      })
      .catch((e) => setError(String(e)))
  }, [])

  useEffect(() => {
    load()
    return onSignedOut(load)
  }, [load])

  const signOut = useCallback(async () => {
    await logout()
    load()
  }, [load])

  if (error && !session) return <LoginPage unreachable={error} onRetry={load} onSignedIn={setSession} />
  // One fast request. The empty shell, not a blank page, so the header is
  // already in place when the page arrives.
  if (!session) return <AppShell>{null}</AppShell>
  if (!session.authenticated) {
    return session.mode === 'local' ? <LocalOnlyPage /> : <LoginPage onSignedIn={setSession} />
  }
  return <SessionContext.Provider value={{ session, signOut }}>{children}</SessionContext.Provider>
}
