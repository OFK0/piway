// Which link clicks the dashboard handles itself (lib/navigation.ts). Kept
// free of React and the DOM so it is unit-tested with node --test.

/**
 * Whether a click on a link to href should be handled here instead of by the
 * browser. Exported for tests; takes plain values so it runs without a DOM.
 */
export function shouldIntercept(
  click: { button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; defaultPrevented: boolean },
  link: { href: string; target: string; download: boolean },
  here: { origin: string; pathname: string; search: string },
): boolean {
  if (click.defaultPrevented || click.button !== 0) return false
  if (click.metaKey || click.ctrlKey || click.shiftKey || click.altKey) return false // new tab or window, download
  if (link.download || (link.target !== '' && link.target !== '_self')) return false
  let url: URL
  try {
    url = new URL(link.href)
  } catch {
    return false
  }
  if (url.origin !== here.origin) return false
  if (!isPagePath(url.pathname)) return false
  // "#main" on the same page is an in-page jump; leave it to the browser.
  if (url.hash && url.pathname === here.pathname && url.search === here.search) return false
  return true
}

/** Dashboard pages have no extension and are not under /api/; everything else is a file or an endpoint. */
export function isPagePath(pathname: string): boolean {
  if (pathname.startsWith('/api/') || pathname === '/api') return false
  const last = pathname.slice(pathname.lastIndexOf('/') + 1)
  return !last.includes('.')
}
