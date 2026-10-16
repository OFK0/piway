import { lazy, Suspense, type ComponentType } from 'react'

import { AppShell } from '@/components/app-shell'
import { SessionGate } from '@/components/session-gate'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { setPreload, usePathname } from '@/lib/navigation'
import { matchRoute, type Route } from '@/lib/routes'
import { OverviewPage } from '@/pages/overview'

// A page loaded on demand. Once preload() has resolved, Page renders the
// module directly: React.lazy would still suspend for one render and flash
// the fallback even though the code is already here.
function onDemand<P extends object>(load: () => Promise<{ default: ComponentType<P> }>) {
  let loaded: ComponentType<P> | null = null
  let pending: Promise<unknown> | null = null
  const preload = () =>
    (pending ??= load().then(
      (m) => (loaded = m.default),
      (e) => {
        pending = null // let a later visit try again
        throw e
      },
    ))
  const Lazy = lazy(load)
  const Page = (props: P) => {
    const Loaded = loaded
    return Loaded ? <Loaded {...props} /> : <Lazy {...props} />
  }
  return { Page, preload }
}

// Pages other than the overview load on demand, so the overview's bundle
// stays small.
const pages = {
  'design-system': onDemand(() => import('@/pages/design-system')),
  apps: onDemand(() => import('@/pages/apps')),
  app: onDemand(() => import('@/pages/app-detail')),
  build: onDemand(() => import('@/pages/build')),
  databases: onDemand(() => import('@/pages/databases')),
  settings: onDemand(() => import('@/pages/settings')),
}

// Navigation loads the next page's code before switching, so the current
// page stays on screen until the next one can draw (lib/navigation.ts).
setPreload((path) => {
  const route = matchRoute(path)
  return route.page in pages ? pages[route.page as keyof typeof pages].preload() : Promise.resolve()
})

export default function App() {
  return (
    <SessionGate>
      <Page />
    </SessionGate>
  )
}

const { Page: DesignSystemPage } = pages['design-system']
const { Page: AppsPage } = pages.apps
const { Page: AppDetailPage } = pages.app
const { Page: BuildPage } = pages.build
const { Page: SettingsPage } = pages.settings
const { Page: DatabasesPage } = pages.databases

function Page() {
  const route: Route = matchRoute(usePathname())
  if (route.page === 'overview') return <OverviewPage />
  if (route.page === 'not-found') return <NotFound />
  return (
    // Only seen on a first load straight into one of these pages: later page
    // changes wait for the code instead (lib/navigation.ts).
    <Suspense fallback={<AppShell>{null}</AppShell>}>
      {route.page === 'design-system' && <DesignSystemPage />}
      {route.page === 'apps' && <AppsPage />}
      {/* A different app or build is a different page: start it fresh. */}
      {route.page === 'app' && <AppDetailPage key={route.name} name={route.name} />}
      {route.page === 'build' && <BuildPage key={route.id} id={route.id} />}
      {route.page === 'settings' && <SettingsPage />}
      {route.page === 'databases' && <DatabasesPage />}
    </Suspense>
  )
}

function NotFound() {
  const path = usePathname()
  return (
    <AppShell>
      <Empty className="border">
        <EmptyHeader>
          <EmptyTitle>
            <h1 className="type-title">Page not found</h1>
          </EmptyTitle>
          <EmptyDescription>There is nothing at {path}.</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <Button render={<a href="/" />} nativeButton={false}>
            Go to the overview
          </Button>
        </EmptyContent>
      </Empty>
    </AppShell>
  )
}
