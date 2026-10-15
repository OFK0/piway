import type { AppInfo } from '@/api'

/** Where an app is reached: its published URL as a link, else its local port. */
export function AppAddress({ app }: { app: AppInfo }) {
  if (app.url) {
    return (
      <a href={app.url} target="_blank" rel="noreferrer" className="w-fit break-all underline underline-offset-4 hover:text-foreground">
        {app.url.replace(/^https?:\/\//, '')}
      </a>
    )
  }
  return (
    <span>
      127.0.0.1:<span className="tabular-nums">{app.port}</span>
    </span>
  )
}
