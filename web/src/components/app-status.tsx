import { CheckCircleIcon, CircleIcon, WarningCircleIcon } from '@phosphor-icons/react'

import type { AppInfo } from '@/api'
import { BuildState } from '@/components/build-state'
import { Badge } from '@/components/ui/badge'

/** True while a build or rollback of the app is queued or running. */
export const isBusy = (a: AppInfo) => a.last?.state === 'queued' || a.last?.state === 'running'

// What a person wants to know first: is it being deployed, and if not, is
// it up? A container that should run but does not is the one alarm.
export function AppStatus({ app }: { app: AppInfo }) {
  if (app.last && isBusy(app)) return <BuildState build={app.last} />
  if (app.container?.state === 'running') {
    return (
      <Badge variant="secondary">
        <CheckCircleIcon data-icon="inline-start" />
        Running
      </Badge>
    )
  }
  if (app.live) {
    return (
      <Badge variant="destructive">
        <WarningCircleIcon data-icon="inline-start" weight="bold" />
        Down
      </Badge>
    )
  }
  return (
    <Badge variant="outline">
      <CircleIcon data-icon="inline-start" />
      Never deployed
    </Badge>
  )
}
