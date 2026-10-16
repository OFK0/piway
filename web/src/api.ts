import { needsSignIn, notifySignedOut } from './lib/session.ts'

// Types and calls for piway's HTTP API. The schema is documented in
// docs/API.md; keep the two in sync. Every metric may be null (collector
// unavailable, failed this round, or no baseline yet).

export interface CPU {
  total: number
  per_core: number[]
}

export interface Memory {
  total_bytes: number
  free_bytes: number
  available_bytes: number
  used_bytes: number
  buffers_bytes: number
  cached_bytes: number
  swap_total_bytes: number
  swap_free_bytes: number
  swap_used_bytes: number
}

export interface ThrottleState {
  under_voltage: boolean
  freq_capped: boolean
  throttled: boolean
  soft_temp_limit: boolean
}

export interface Thermal {
  temp_celsius: number | null
  throttle: {
    raw: number
    now: ThrottleState
    since_boot: ThrottleState
  } | null
}

export interface DiskDevice {
  name: string
  read_bytes_per_sec: number
  write_bytes_per_sec: number
  read_ops_per_sec: number
  write_ops_per_sec: number
  busy_percent: number
  written_bytes: number
}

export interface Filesystem {
  path: string
  total_bytes: number
  used_bytes: number
  avail_bytes: number
  used_percent: number
}

export interface NetInterface {
  name: string
  rx_bytes_per_sec: number
  tx_bytes_per_sec: number
  rx_packets_per_sec: number
  tx_packets_per_sec: number
  rx_errors: number
  tx_errors: number
  rx_dropped: number
  tx_dropped: number
}

export interface Snapshot {
  seq: number
  time: string
  cpu: CPU | null
  memory: Memory | null
  thermal: Thermal | null
  disk: { devices: DiskDevice[] | null; filesystems: Filesystem[] } | null
  network: NetInterface[] | null
  errors?: Record<string, string>
}

export interface Health {
  status: string
  version: string
  commit: string
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal })
  if (needsSignIn(res.status)) notifySignedOut()
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error ?? `${path}: HTTP ${res.status}`)
  }
  return res.json() as Promise<T>
}

export const fetchSnapshot = (signal?: AbortSignal) => getJSON<Snapshot>('/api/v1/snapshot', signal)
export const fetchHealth = (signal?: AbortSignal) => getJSON<Health>('/healthz', signal)

/** GET /api/v1/series (docs/API.md). Points are [time_ms, avg, min, max]. */
export interface SeriesResponse {
  resolution: 'raw' | '1m' | '1h'
  step_seconds: number
  from: string
  to: string
  series: { name: string; points: [number, number, number, number][] }[]
}

/** Thrown when the server records no history (data_dir is empty). */
export class HistoryDisabledError extends Error {}

export async function fetchSeries(
  name: string,
  params: { from: string; to?: string; step?: string },
  signal?: AbortSignal,
): Promise<SeriesResponse> {
  const q = new URLSearchParams({ name, ...params })
  const res = await fetch(`/api/v1/series?${q}`, { signal })
  if (needsSignIn(res.status)) notifySignedOut()
  if (res.status === 503) throw new HistoryDisabledError('History is off (data_dir is empty).')
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error ?? `/api/v1/series: HTTP ${res.status}`)
  }
  return res.json() as Promise<SeriesResponse>
}

// --- Deploys (docs/API.md › Builds) ---------------------------------------

export type BuildState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

export interface Build {
  id: string
  app?: string
  repo: string
  branch: string
  commit: string
  pusher?: string
  state: BuildState
  kind?: 'rollback' | 'restart'
  rollback_of?: string
  image?: string
  /** Set for a static site: the build whose copy of the site this one serves. */
  site?: string
  error?: string
  queued_at: string
  started_at?: string
  finished_at?: string
}

export interface AppInfo {
  name: string
  repo: string
  branch: string
  port: number
  /** Set when apps are published through Caddy (proxy_domain). */
  url?: string
  live?: Build
  last?: Build
  container?: { name: string; state: string; image: string; started_at?: string }
}

/** Thrown when piway has no Docker daemon, so deploys are off (HTTP 503). */
export class DeploysDisabledError extends Error {}

async function deployJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init)
  const body = await res.json().catch(() => null)
  if (needsSignIn(res.status)) notifySignedOut()
  if (res.status === 503) throw new DeploysDisabledError(body?.error ?? 'Deploys are off.')
  if (!res.ok) throw new Error(body?.error ?? `${path}: HTTP ${res.status}`)
  return body as T
}

export const fetchApps = (signal?: AbortSignal) =>
  deployJSON<{ apps: AppInfo[] }>('/api/v1/apps', { signal }).then((r) => r.apps)
export const fetchBuilds = (signal?: AbortSignal) =>
  deployJSON<{ builds: Build[] }>('/api/v1/builds', { signal }).then((r) => r.builds)
export const fetchBuild = (id: string, signal?: AbortSignal) =>
  deployJSON<Build>(`/api/v1/builds/${encodeURIComponent(id)}`, { signal })
export const cancelBuild = (id: string) =>
  deployJSON<unknown>(`/api/v1/builds/${encodeURIComponent(id)}/cancel`, { method: 'POST' })
export const rollbackApp = (name: string) =>
  deployJSON<Build>(`/api/v1/apps/${encodeURIComponent(name)}/rollback`, { method: 'POST' })
export const redeployApp = (name: string) =>
  deployJSON<Build>(`/api/v1/apps/${encodeURIComponent(name)}/redeploy`, { method: 'POST' })

// --- Environment (docs/API.md › /api/v1/apps/{name}/env) -------------------

export interface EnvVar {
  name: string
  value: string
}
export interface AppSecret {
  name: string
  set: boolean
  /** Listed in the app's run.secrets; an undeclared one is not passed to it. */
  declared: boolean
  /** Only for a signed-in browser, and only when set. */
  value?: string
}
/** A variable piway sets, such as DATABASE_URL for run.databases. */
export interface InjectedVar {
  name: string
  /** What sets it, such as "postgres". */
  from: string
  /** False until the first deploy that sets it up. */
  set: boolean
  /** Only for a signed-in browser, and only when set. */
  value?: string
}
export interface AppEnv {
  app: string
  build: string
  static: boolean
  env: EnvVar[]
  secrets: AppSecret[]
  injected: InjectedVar[]
  restart_needed: boolean
  file: string
  values_shown: boolean
}
const envPath = (app: string) => `/api/v1/apps/${encodeURIComponent(app)}`
export const fetchAppEnv = (app: string, signal?: AbortSignal) => deployJSON<AppEnv>(`${envPath(app)}/env`, { signal })
export const setAppSecret = (app: string, name: string, value: string) =>
  deployJSON<AppEnv>(`${envPath(app)}/secrets/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value }),
  })
export const removeAppSecret = (app: string, name: string) =>
  deployJSON<AppEnv>(`${envPath(app)}/secrets/${encodeURIComponent(name)}`, { method: 'DELETE' })

// --- Volumes (docs/API.md › /api/v1/apps/{name}/volumes) -------------------

export interface AppVolume {
  name: string
  /** Where the live version mounts it; absent for one it no longer declares. */
  path?: string
  docker: string
  declared: boolean
  /** False for a declared volume no release has made yet. */
  created: boolean
  /** Bytes; -1 when unknown. */
  size_bytes: number
  in_use: boolean
}
export const fetchAppVolumes = (app: string, signal?: AbortSignal) =>
  deployJSON<{ volumes: AppVolume[] }>(`${envPath(app)}/volumes`, { signal }).then((r) => r.volumes)
export const removeAppVolume = (app: string, volume: string) =>
  deployJSON<unknown>(`${envPath(app)}/volumes/${encodeURIComponent(volume)}`, { method: 'DELETE' })

// --- Databases (docs/API.md › /api/v1/databases) ---------------------------

export interface DatabaseApp {
  name: string
  backups: number
  last_backup?: string
  last_backup_bytes?: number
}
export interface DatabaseEngine {
  name: 'postgres' | 'mariadb' | 'redis'
  /** "not_started" until an app asks for it, else the container's state. */
  state: string
  image?: string
  pinned: string
  upgrade_available: boolean
  volume?: string
  /** -1 when unknown. */
  size_bytes: number
  memory_bytes: number
  memory_limit_bytes: number
  apps: DatabaseApp[]
}
export const fetchDatabases = (signal?: AbortSignal) =>
  deployJSON<{ engines: DatabaseEngine[] }>('/api/v1/databases', { signal }).then((r) => r.engines)
export const backupDatabases = () => deployJSON<{ done: string[] }>('/api/v1/databases/backup', { method: 'POST' })

export function buildLogStreamURL(id: string, loc: { protocol: string; host: string } = window.location): string {
  return `${loc.protocol === 'https:' ? 'wss' : 'ws'}://${loc.host}/api/v1/builds/${encodeURIComponent(id)}/log/stream`
}

// --- Alerts (docs/API.md › GET /api/v1/alerts) -----------------------------

export type AlertState = 'pending' | 'firing' | 'clearing' | 'resolved'

export interface Alert {
  rule: string
  /** The metric series, or deploy:<repo>@<branch>. */
  series: string
  summary: string
  state: AlertState
  value?: number
  since: string
  fired_at?: string
}

export interface AlertEvent extends Alert {
  at: string
}

export interface AlertsResponse {
  active: Alert[]
  events: AlertEvent[]
  rules: { name: string; metric: string; condition: string; for: string; clear_for: string }[]
  deploy_failures: boolean
}

export const fetchAlerts = (signal?: AbortSignal) => getJSON<AlertsResponse>('/api/v1/alerts', signal)

export const restartApp = (name: string) =>
  deployJSON<Build>(`/api/v1/apps/${encodeURIComponent(name)}/restart`, { method: 'POST' })
/** Deploys the newest commit of the app's branch, or `commit` (a full SHA). */
export const deployApp = (name: string, commit?: string) =>
  deployJSON<Build>(`/api/v1/apps/${encodeURIComponent(name)}/deploy`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(commit ? { commit } : {}),
  })
export interface DeployTarget {
  repo: string
  branch: string
  app?: string
}
export const fetchTargets = (signal?: AbortSignal) =>
  deployJSON<{ targets: DeployTarget[] }>('/api/v1/targets', { signal }).then((r) => r.targets)

// --- Repositories (docs/API.md › /api/v1/repos) ----------------------------

/** How piway reads a private repository; absent for a public one. */
export type RepoAuth = 'deploy-key' | 'token'
export interface Repo {
  repo: string
  branch: string
  /** The app this branch became, once it has been built. */
  app?: string
  auth?: RepoAuth
  /** For a deploy key: the public key to add on GitHub, and the page to add it on. */
  public_key?: string
  deploy_key_url?: string
}
export interface ReposResponse {
  repos: Repo[]
  webhook: { path: string; configured: boolean }
  /** Whether private repositories can be added, and whether a token is stored. */
  private?: { available: boolean; token_set: boolean }
}
export const fetchRepos = (signal?: AbortSignal) => getJSON<ReposResponse>('/api/v1/repos', signal)

async function repoRequest<T = unknown>(path: string, init: RequestInit): Promise<T> {
  const res = await fetch(path, init)
  const body = await res.json().catch(() => null)
  if (needsSignIn(res.status)) notifySignedOut()
  if (!res.ok) throw new Error(body?.error ?? `${path}: HTTP ${res.status}`)
  return body as T
}
const jsonInit = (method: string, body: unknown): RequestInit => ({
  method,
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
})
export const addRepo = (repo: string, branch: string, auth?: RepoAuth) =>
  repoRequest<Repo>('/api/v1/repos', jsonInit('POST', auth ? { repo, branch, auth } : { repo, branch }))

/** The answer to "Test access": kind and message say what to fix. */
export interface AccessResult {
  ok: boolean
  kind?: string
  message?: string
  /** git's own words. */
  detail?: string
}
export const testRepoAccess = (repo: string) =>
  repoRequest<AccessResult>('/api/v1/repos/test', jsonInit('POST', { repo }))
export const setRepoToken = (token: string) => repoRequest('/api/v1/repos/token', jsonInit('PUT', { token }))
export const removeRepo = (repo: string, branch: string) =>
  repoRequest<{ key_removed?: boolean }>(`/api/v1/repos?${new URLSearchParams({ repo, branch })}`, { method: 'DELETE' })
