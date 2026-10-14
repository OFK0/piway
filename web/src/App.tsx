import { lazy, Suspense } from 'react'

import { AppShell } from '@/components/app-shell'
import { OverviewPage } from '@/pages/overview'

// The design system page is a reference for developers; keep it out of the
// main bundle.
const DesignSystemPage = lazy(() => import('@/pages/design-system'))

// Two routes do not need a router yet. Links are plain <a href>, and piway
// serves index.html for unknown paths (#12).
export default function App() {
  if (window.location.pathname.replace(/\/$/, '') === '/design-system') {
    return (
      // The empty shell keeps the header and footer in place while the chunk loads.
      <Suspense fallback={<AppShell>{null}</AppShell>}>
        <DesignSystemPage />
      </Suspense>
    )
  }
  return <OverviewPage />
}
