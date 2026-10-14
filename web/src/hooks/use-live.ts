import { useEffect, useSyncExternalStore } from 'react'

import { LiveStream, streamURL, type LiveState } from '@/lib/live-stream'

// One WebSocket for the whole page, shared by every component that reads
// live data. It opens with the first user and closes with the last.
let shared: LiveStream | null = null
let users = 0

const onOnline = () => shared?.setOnline(true)
const onOffline = () => shared?.setOnline(false)

function stream(): LiveStream {
  shared ??= new LiveStream({ url: streamURL() })
  return shared
}

function acquire() {
  if (users++ === 0) {
    stream().start()
    window.addEventListener('online', onOnline)
    window.addEventListener('offline', onOffline)
  }
}

function release() {
  if (--users === 0) {
    stream().stop()
    window.removeEventListener('online', onOnline)
    window.removeEventListener('offline', onOffline)
  }
}

export function useLive(): LiveState & { retryNow: () => void } {
  const s = stream()
  useEffect(() => {
    acquire()
    return release
  }, [])
  const state = useSyncExternalStore(s.subscribe, s.getState)
  return { ...state, retryNow: () => s.retryNow() }
}
