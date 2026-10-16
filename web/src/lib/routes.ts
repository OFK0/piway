// The dashboard's pages. Links are plain <a href>, turned into in-page
// navigation by lib/navigation.ts; piway serves index.html for every path
// without an extension (#12), so a reload or a shared link works anywhere.
export type Route =
  | { page: 'overview' }
  | { page: 'apps' }
  | { page: 'app'; name: string }
  | { page: 'build'; id: string }
  | { page: 'databases' }
  | { page: 'design-system' }
  | { page: 'settings' }
  | { page: 'not-found' }

export function matchRoute(pathname: string): Route {
  const parts = pathname.split('/').filter(Boolean).map(decodeURIComponent)
  const [first, second, ...rest] = parts
  if (rest.length > 0) return { page: 'not-found' }
  switch (first) {
    case undefined:
      return { page: 'overview' }
    case 'apps':
      return second === undefined ? { page: 'apps' } : { page: 'app', name: second }
    case 'builds':
      return second === undefined ? { page: 'not-found' } : { page: 'build', id: second }
    case 'databases':
      return second === undefined ? { page: 'databases' } : { page: 'not-found' }
    case 'design-system':
      return second === undefined ? { page: 'design-system' } : { page: 'not-found' }
    case 'settings':
      return second === undefined ? { page: 'settings' } : { page: 'not-found' }
    default:
      return { page: 'not-found' }
  }
}

export const appPath = (name: string) => `/apps/${encodeURIComponent(name)}`
export const buildPath = (id: string) => `/builds/${encodeURIComponent(id)}`
