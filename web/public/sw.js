// piway's service worker: shows Web Push notifications (internal/push),
// also when no dashboard tab is open, and opens the dashboard on a click.
// It caches nothing: the dashboard needs the live API anyway.

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()))

self.addEventListener('push', (event) => {
  let msg = { title: 'piway', body: '' }
  try {
    msg = { ...msg, ...event.data.json() }
  } catch {
    if (event.data) msg.body = event.data.text()
  }
  event.waitUntil(
    self.registration.showNotification(msg.title, {
      body: msg.body,
      tag: msg.tag,
      // A newer notification with the same tag (the alert resolving)
      // replaces the older one; renotify still makes it alert again.
      renotify: Boolean(msg.tag),
      icon: '/icon-192.png',
      badge: '/icon-192.png',
      data: { url: msg.url || '/' },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const url = new URL(event.notification.data?.url || '/', self.location.origin).href
  event.waitUntil(
    (async () => {
      const tabs = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      const tab = tabs.find((c) => new URL(c.url).origin === self.location.origin)
      if (tab) {
        await tab.focus()
        return tab.navigate(url)
      }
      return self.clients.openWindow(url)
    })(),
  )
})
