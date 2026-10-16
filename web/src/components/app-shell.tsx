import { DatabaseIcon, GearIcon, HouseIcon, SignOutIcon, StackIcon } from '@phosphor-icons/react'
import type { Icon } from '@phosphor-icons/react'
import type { ReactNode } from 'react'

import { Logo } from '@/components/logo'
import { useSession } from '@/components/session-gate'
import { ThemeToggle } from '@/components/theme-toggle'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { usePathname, usePendingNavigation } from '@/lib/navigation'
import { cn } from '@/lib/utils'

// On a phone the links are icons: four words plus the buttons need about
// 435 px, and a 390 px screen would slide sideways (#150).
const nav: { href: string; label: string; icon: Icon; match: (p: string) => boolean }[] = [
  { href: '/', label: 'Overview', icon: HouseIcon, match: (p) => p === '/' },
  { href: '/apps', label: 'Apps', icon: StackIcon, match: (p) => p.startsWith('/apps') || p.startsWith('/builds') },
  { href: '/databases', label: 'Databases', icon: DatabaseIcon, match: (p) => p === '/databases' },
  { href: '/settings', label: 'Settings', icon: GearIcon, match: (p) => p === '/settings' },
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
  const pathname = usePathname()
  const pending = usePendingNavigation()
  return (
    <div className="flex min-h-svh flex-col">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:bg-background focus:px-3 focus:py-2"
      >
        Skip to content
      </a>
      <header className="sticky top-0 z-40 [view-transition-name:site-header] border-b bg-background/95 backdrop-blur supports-backdrop-filter:bg-background/80">
        <div className="mx-auto flex h-14 w-full max-w-7xl items-center gap-2 px-4 md:px-6">
          <a href="/" aria-label="piway overview">
            <Logo mark className="sm:hidden" />
            <Logo className="hidden sm:block" />
          </a>
          <nav aria-label="Main" className="ml-1 flex min-w-0 items-center sm:ml-4 sm:gap-1">
            {nav.map((item) => {
              const current = item.match(pathname)
              return (
                <a
                  key={item.href}
                  href={item.href}
                  aria-current={current ? 'page' : undefined}
                  aria-label={item.label}
                  className={cn(
                    'flex size-9 items-center justify-center text-lg text-muted-foreground underline-offset-8 hover:text-foreground sm:size-auto sm:px-2 sm:py-1 sm:type-label',
                    current && 'border-b-2 border-brand text-foreground sm:border-b-0 sm:underline sm:decoration-brand sm:decoration-2',
                  )}
                >
                  <item.icon className="sm:hidden" aria-hidden />
                  <span className="hidden sm:inline">{item.label}</span>
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
        {/* A page change waiting for code on a slow network (lib/navigation.ts). */}
        {pending && (
          <div role="progressbar" aria-label="Loading page" className="absolute inset-x-0 -bottom-px h-0.5 overflow-hidden">
            <div className="h-full w-1/3 animate-[nav-progress_1s_ease-in-out_infinite] bg-brand" />
          </div>
        )}
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
