import { lazy, Suspense } from 'react'

import { OverviewPage } from '@/pages/overview'

// The design system page is a reference for developers; keep it out of the
// main bundle.
const DesignSystemPage = lazy(() => import('@/pages/design-system'))

// Two routes do not need a router yet. Links are plain <a href>, and piway
// serves index.html for unknown paths (#12).
export default function App() {
  if (window.location.pathname.replace(/\/$/, '') === '/design-system') {
    return (
      <Suspense fallback={null}>
        <DesignSystemPage />
      </Suspense>
    )
  }
  return <OverviewPage />
}
