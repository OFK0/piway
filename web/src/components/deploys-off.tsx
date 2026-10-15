import { CubeIcon, RocketLaunchIcon } from '@phosphor-icons/react'

import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'

const docs = 'https://github.com/OFK0/piway/blob/main/docs/'

export function DeploysOff({ reason }: { reason: string }) {
  return (
    <Empty className="border">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <CubeIcon />
        </EmptyMedia>
        <EmptyTitle>Deploys are off</EmptyTitle>
        <EmptyDescription>{reason} piway runs apps with rootless Docker; set it up, then restart piway.</EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button render={<a href={docs + 'DOCKER.md'} target="_blank" rel="noreferrer" />} nativeButton={false}>
          Set up Docker
        </Button>
      </EmptyContent>
    </Empty>
  )
}

export function NoApps() {
  return (
    <Empty className="border">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <RocketLaunchIcon />
        </EmptyMedia>
        <EmptyTitle>No apps yet</EmptyTitle>
        <EmptyDescription>
          Add a piway.yaml to a repository, list it in apps.yaml and push: the app appears here once it is built.
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button render={<a href={docs + 'DEPLOY.md'} target="_blank" rel="noreferrer" />} nativeButton={false}>
          Read the deploy guide
        </Button>
      </EmptyContent>
    </Empty>
  )
}
