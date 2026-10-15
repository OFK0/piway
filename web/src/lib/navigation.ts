import { useSyncExternalStore } from 'react'
import { flushSync } from 'react-dom'

import { shouldIntercept } from './links.ts'

// Client-side navigation. Links stay plain <a href>, so they work without
// JavaScript, open in a new tab and show their address on hover; a click on
// one that leads to another dashboard page is caught and turned into a
// history entry instead of a full page load (#87).
//
// A page change waits until the next page's code has loaded, so the old page
// stays on screen until the new one can draw in a single frame: no empty
// shell, no blank page, and the header never leaves.

let current = typeof window === 'undefined' ? '/' : window.location.pathname
const listeners = new Set<() => void>()
let preload: (path: string) => Promise<unknown> = () => Promise.resolve()
// Each navigation takes a ticket; only the newest one may commit, so a slow
// chunk cannot land after a later click.
let ticket = 0
// True while a page change waits for code for longer than pendingDelay, so
// a slow network shows progress; a fast one shows nothing.
let pending = false
const pendingDelay = 150

/** The path of the page on screen. It changes only once that page can render. */
export function usePathname(): string {
  return useSyncExternalStore(subscribe, () => current)
}

/** Whether a page change is waiting for the next page's code. */
export function usePendingNavigation(): boolean {
  return useSyncExternalStore(subscribe, () => pending)
}

function setPending(p: boolean) {
  if (p === pending) return
  pending = p
  for (const l of listeners) l()
}

// Loads path's code, reporting progress if it takes a while. Returns whether
// this navigation is still the newest one.
async function load(path: string, mine: number): Promise<boolean> {
  const timer = setTimeout(() => mine === ticket && setPending(true), pendingDelay)
  await preload(path).catch(() => {}) // a failed chunk is reported by the page's own Suspense
  clearTimeout(timer)
  if (mine !== ticket) return false
  setPending(false)
  return true
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

/** Tells navigation how to load a page's code before showing it. */
export function setPreload(fn: (path: string) => Promise<unknown>): void {
  preload = fn
}

/** Shows the page at href, as a new history entry unless replace is set. */
export async function navigate(href: string, { replace = false } = {}): Promise<void> {
  const url = new URL(href, window.location.href)
  // A link to the page already on screen does not add a history entry.
  if (url.href === window.location.href) replace = true
  if (!(await load(url.pathname, ++ticket))) return
  commit(() => {
    // Remember where this page was scrolled, for Back.
    window.history.replaceState({ ...window.history.state, scrollY: window.scrollY }, '')
    if (replace) window.history.replaceState(null, '', url)
    else window.history.pushState(null, '', url)
    show(url.pathname)
    scrollTo(url.hash, 0)
  })
}

// Back and Forward: the address bar has already changed; the page follows as
// soon as its code is ready, at the position it was left.
async function onPopState(e: PopStateEvent) {
  const path = window.location.pathname
  const y = typeof e.state?.scrollY === 'number' ? e.state.scrollY : 0
  if (!(await load(path, ++ticket))) return
  commit(() => {
    show(path)
    scrollTo('', y)
  })
}

// Renders the page for path before returning, so whatever follows (scrolling
// to a position) already sees the new page in the DOM. Inside flushSync the
// render happens when the callback ends, not when a listener is called.
function show(path: string) {
  flushSync(() => {
    current = path
    for (const l of listeners) l()
  })
}

function scrollTo(hash: string, y: number) {
  const target = hash && document.getElementById(decodeURIComponent(hash.slice(1)))
  if (target) target.scrollIntoView()
  else window.scrollTo(0, y)
}

// Runs a page change inside a view transition where the browser has them and
// the user has not asked for reduced motion: old and new page cross-fade
// (index.css), and the header, identical in both, stays still.
function commit(update: () => void) {
  const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  if (typeof document.startViewTransition === 'function' && !reduce) {
    document.startViewTransition(update)
  } else {
    update()
  }
}

/** Catches clicks on dashboard links anywhere in the document. Call once. */
export function interceptLinks(): void {
  window.history.scrollRestoration = 'manual'
  window.addEventListener('popstate', (e) => void onPopState(e))
  document.addEventListener('click', (e) => {
    const a = e.target instanceof Element ? e.target.closest('a[href]') : null
    if (!(a instanceof HTMLAnchorElement)) return
    if (!shouldIntercept(e, { href: a.href, target: a.target, download: a.hasAttribute('download') }, window.location)) return
    e.preventDefault()
    void navigate(a.href)
  })
}
