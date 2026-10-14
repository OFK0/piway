import type { ReactNode } from 'react'

import { Logo } from '@/components/logo'
import { ThemeToggle } from '@/components/theme-toggle'
import { Separator } from '@/components/ui/separator'

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
          <div className="ml-auto flex items-center gap-2">
            {props.actions}
            <ThemeToggle />
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
          {/* Last, so the link does not move when loaded content of unknown
              width (the version) arrives. */}
          {props.footer}
        </div>
      </footer>
    </div>
  )
}
