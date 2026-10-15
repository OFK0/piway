import { SignOutIcon } from '@phosphor-icons/react'
import type { ReactNode } from 'react'

import { Logo } from '@/components/logo'
import { useSession } from '@/components/session-gate'
import { ThemeToggle } from '@/components/theme-toggle'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { cn } from '@/lib/utils'

const nav = [
  { href: '/', label: 'Overview', match: (p: string) => p === '/' },
  { href: '/apps', label: 'Apps', match: (p: string) => p.startsWith('/apps') || p.startsWith('/builds') },
]

// Only with a password: in local mode there is nothing to sign out of.
function SignOut() {
  const ctx = useSession()
  if (ctx?.session.mode !== 'password') return null
  return (
    <Button variant="ghost" size="icon" aria-label="Sign out" title="Sign out" onClick={() => void ctx.signOut()}>
      <SignOutIcon />
    </Button>
  )
}

// Page frame: a hairline header with the brand and global controls, a
// left-aligned content column, and a quiet footer.
export function AppShell(props: { actions?: ReactNode; footer?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex min-h-svh flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:bg-background focus:px-3 focus:py-2"
      >
        Skip to content
      </a>
      <header className="sticky top-0 z-40 border-b bg-background/95 backdrop-blur supports-backdrop-filter:bg-background/80">
        <div className="mx-auto flex h-14 w-full max-w-7xl items-center gap-2 px-4 md:px-6">
          <a href="/" aria-label="piway overview">
            <Logo />
          </a>
          <nav aria-label="Main" className="ml-4 flex items-center gap-1">
            {nav.map((item) => {
              const current = item.match(window.location.pathname)
              return (
                <a
                  key={item.href}
                  href={item.href}
                  aria-current={current ? 'page' : undefined}
                  className={cn(
                    'px-2 py-1 type-label text-muted-foreground underline-offset-8 hover:text-foreground',
                    current && 'text-foreground underline decoration-brand decoration-2',
                  )}
                >
                  {item.label}
                </a>
              )
            })}
          </nav>
          <div className="ml-auto flex items-center gap-2">
            {props.actions}
            <ThemeToggle />
            <SignOut />
          </div>
        </div>
      </header>

      <main id="main" className="mx-auto flex w-full max-w-7xl flex-1 flex-col gap-8 px-4 py-6 md:px-6 md:py-8">
        {props.children}
      </main>

      <footer className="mx-auto w-full max-w-7xl px-4 pb-6 md:px-6">
        <Separator className="mb-4" />
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 type-caption text-muted-foreground">
          <a href="/design-system" className="underline-offset-4 hover:text-foreground hover:underline">
            Design system
          </a>
          {/* Generated at build time (make build); absent on the dev server. */}
          <a href="/THIRD_PARTY_NOTICES.txt" className="underline-offset-4 hover:text-foreground hover:underline">
            Licenses
          </a>
          {/* Last, so the link does not move when loaded content of unknown
              width (the version) arrives. */}
          {props.footer}
        </div>
      </footer>
    </div>
  )
}
