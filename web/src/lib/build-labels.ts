import type { Build } from '../api.ts'

type Kinded = Pick<Build, 'kind' | 'pusher'>

/** What started a build: "Push", "Deploy" (API or MCP), "Redeploy", "Rollback" or "Restart". */
export function buildCause(b: Kinded): string {
  if (b.kind === 'rollback') return 'Rollback'
  if (b.kind === 'restart') return 'Restart'
  if (b.pusher === 'dashboard') return 'Redeploy'
  if (b.pusher === 'api') return 'Deploy'
  return 'Push'
}

/** The past tense for a finished build: "deployed", "rolled back", "restarted". */
export function doneVerb(b: Kinded): string {
  if (b.kind === 'rollback') return 'rolled back'
  if (b.kind === 'restart') return 'restarted'
  return 'deployed'
}

/** The progressive form while it runs: "Building", "Rolling back", "Restarting". */
export function runningVerb(b: Kinded): string {
  if (b.kind === 'rollback') return 'Rolling back'
  if (b.kind === 'restart') return 'Restarting'
  return 'Building'
}

export const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)
