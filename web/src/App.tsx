import { lazy, Suspense } from 'react'

import { AppShell } from '@/components/app-shell'
import { SessionGate } from '@/components/session-gate'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { matchRoute } from '@/lib/routes'
import { OverviewPage } from '@/pages/overview'

// Pages other than the overview load on demand, so the overview's bundle
// stays small.
const DesignSystemPage = lazy(() => import('@/pages/design-system'))
const AppsPage = lazy(() => import('@/pages/apps'))
const AppDetailPage = lazy(() => import('@/pages/app-detail'))
const BuildPage = lazy(() => import('@/pages/build'))

// Links are plain <a href>, and piway serves index.html for unknown paths
// (#12), so routing is just reading the path once.
export default function App() {
  return (
    <SessionGate>
      <Page />
    </SessionGate>
  )
}

function Page() {
  const route = matchRoute(window.location.pathname)
  if (route.page === 'overview') return <OverviewPage />
  return (
    // The empty shell keeps the header and footer in place while a chunk loads.
    <Suspense fallback={<AppShell>{null}</AppShell>}>
      {route.page === 'design-system' && <DesignSystemPage />}
      {route.page === 'apps' && <AppsPage />}
      {route.page === 'app' && <AppDetailPage name={route.name} />}
      {route.page === 'build' && <BuildPage id={route.id} />}
      {route.page === 'not-found' && <NotFound />}
    </Suspense>
  )
}

function NotFound() {
  return (
    <AppShell>
      <Empty className="border">
        <EmptyHeader>
          <EmptyTitle>
            <h1 className="type-title">Page not found</h1>
          </EmptyTitle>
          <EmptyDescription>There is nothing at {window.location.pathname}.</EmptyDescription>
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
