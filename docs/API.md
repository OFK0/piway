# HTTP API

All endpoints except [`/metrics`](#get-metrics) return JSON
(`Content-Type: application/json`). Errors use a single shape:

```json
{ "error": "human-readable message" }
```

Routes only accept the methods listed for them; any other method gets
`405 Method Not Allowed` with an `Allow` header. Paths under `/api/v1` keep a
stable schema: fields may be added, but existing fields are not renamed,
removed or retyped without moving to `/api/v2`.

## Sign-in

Every path under `/api/`, `/metrics` and [`/mcp`](MCP.md) needs a session, except
`/healthz`, the [session endpoints](#session-endpoints) and the
[GitHub webhook](#post-apiv1webhooksgithub) (signed by GitHub instead).
The dashboard's own files are public: they hold no data, and the sign-in
page is one of them.

- **Password set** (`piway -set-password`, see
  [CONFIG.md](CONFIG.md#dashboard-password)): a request needs the
  `piway_session` cookie from `POST /api/v1/login`, or an
  [API token](#api-tokens), else it gets `401`.
- **No password yet**: piway answers only requests from the Pi itself
  (`127.0.0.1` or `::1`), such as through an SSH tunnel. Others get
  `403` with how to set a password. A reverse proxy on the Pi makes every
  request come from loopback, so piway does not count a request as local
  when it carries proxy headers (`X-Forwarded-For`, `Forwarded`,
  `X-Real-Ip`, `Tailscale-User-Login`). See [REMOTE.md](REMOTE.md).

Cross-site requests that change something are rejected with `403`: piway
checks the browser's `Sec-Fetch-Site` and `Origin` headers (CSRF
protection). Clients that send neither, like `curl` or GitHub, are not
affected.

### Session endpoints

| Method and path | Response |
| --------------- | -------- |
| `GET /api/v1/session` | `{"mode": "password" or "local", "authenticated": bool, "expires_at": time}`. `expires_at` only for a password session. |
| `POST /api/v1/login` | Body `{"password": "..."}`. `200` with the session and a `piway_session` cookie (`HttpOnly`, `SameSite=Strict`, `Secure` over HTTPS, 30 days). `401` for a wrong password; `429` with `Retry-After` while locked out; `409` when no password is set; `400` for a malformed body. |
| `POST /api/v1/logout` | `204`; ends the session and clears the cookie. |

After 5 wrong passwords from one address, each further wrong one locks
that address out for twice as long as the last, from 30 s up to an hour;
a right password, or a day without failures, starts over. Passwords are
checked one at a time (each check takes about 0.3 s and 64 MiB on a Pi 4).

### API tokens

For scripts, Prometheus and the MCP server: create a token in the
dashboard (Settings) and send it as a bearer token.

```sh
curl -H "Authorization: Bearer piway_…" http://<pi-address>:8080/api/v1/apps
```

- A token works for everything a session does, from any address, with or
  without a password set. A wrong or revoked token gets `401`, even from
  the Pi itself.
- Tokens start with `piway_`, so secret scanners can spot one in a
  repository. piway stores only their SHA-256 hash, in
  `<data_dir>/tokens.json`; the token is shown once, when it is created.
- Changing the password does not end tokens; revoking does.
- Tokens are managed only from a signed-in browser (or, without a
  password, from the Pi itself). A request that carries a token gets `403`
  here, so a leaked token cannot create more.

| Method and path | Response |
| --------------- | -------- |
| `GET /api/v1/tokens` | `{"tokens": [{"id", "name", "hint", "created_at", "last_used_at"}…]}`, newest first. `hint` is the token's first characters. `last_used_at` is updated at most once a minute on disk. |
| `POST /api/v1/tokens` | Body `{"name": "..."}` (1 to 64 characters). `201` with the token's fields plus `"token"`, the only time it is shown. `409` at 50 tokens. |
| `DELETE /api/v1/tokens/{id}` | `204`; the token stops working at once. `404` for an unknown id. |

## `GET /healthz`

Liveness probe.

```json
{ "status": "ok", "version": "v0.1.0", "commit": "abc1234" }
```

## `GET /api/v1/snapshot`

The most recent collection round. The agent collects every `-interval`
(default `1s`), so polling faster than that returns the same `seq` again.

| Status | Meaning |
| ------ | ------- |
| `200`  | Snapshot body below. Sent with `Cache-Control: no-store`. |
| `503`  | No round has completed yet (only right after startup). Sent with `Retry-After: 1`. |
| `500`  | The snapshot could not be encoded. |

### Example

```json
{
  "seq": 1234,
  "time": "2026-01-01T12:00:00.000000000Z",
  "cpu": { "total": 12.5, "per_core": [10.0, 15.0, 12.0, 13.0] },
  "memory": {
    "total_bytes": 4000000000,
    "free_bytes": 1000000000,
    "available_bytes": 2500000000,
    "used_bytes": 1500000000,
    "buffers_bytes": 100000000,
    "cached_bytes": 1400000000,
    "swap_total_bytes": 0,
    "swap_free_bytes": 0,
    "swap_used_bytes": 0
  },
  "thermal": {
    "temp_celsius": 45.0,
    "throttle": {
      "raw": 0,
      "now": { "under_voltage": false, "freq_capped": false, "throttled": false, "soft_temp_limit": false },
      "since_boot": { "under_voltage": false, "freq_capped": false, "throttled": false, "soft_temp_limit": false }
    }
  },
  "disk": {
    "devices": [
      {
        "name": "mmcblk0",
        "read_bytes_per_sec": 0,
        "write_bytes_per_sec": 4096,
        "read_ops_per_sec": 0,
        "write_ops_per_sec": 1,
        "busy_percent": 0.5,
        "written_bytes": 1000000000
      }
    ],
    "filesystems": [
      { "path": "/", "total_bytes": 30000000000, "used_bytes": 7500000000, "avail_bytes": 21000000000, "used_percent": 26.3 }
    ]
  },
  "network": [
    {
      "name": "eth0",
      "rx_bytes_per_sec": 2000.0,
      "tx_bytes_per_sec": 1000.0,
      "rx_packets_per_sec": 20.0,
      "tx_packets_per_sec": 10.0,
      "rx_errors": 0,
      "tx_errors": 0,
      "rx_dropped": 0,
      "tx_dropped": 0
    }
  ],
  "errors": { "thermal": "vcgencmd: executable file not found in $PATH" }
}
```

### Fields

| Field | Type | Description |
| ----- | ---- | ----------- |
| `seq` | integer | Round number, starting at 1 and increasing by 1 per round. Resets when piway restarts. |
| `time` | string | RFC 3339 timestamp (nanosecond precision) of when the round started. |
| `cpu` | object \| null | Utilisation since the previous round, in percent `[0, 100]`. |
| `cpu.total` | number | All cores combined. |
| `cpu.per_core` | number[] | One entry per core, in kernel order. |
| `memory` | object \| null | From `/proc/meminfo`. All values in bytes. `used_bytes` is `total_bytes - available_bytes`, i.e. memory that cannot be reclaimed without swapping. |
| `thermal` | object \| null | |
| `thermal.temp_celsius` | number \| null | SoC temperature. |
| `thermal.throttle` | object \| null | Decoded firmware throttling bitmask; null on non-Pi hardware. |
| `thermal.throttle.raw` | integer | The raw `get_throttled` bitmask. |
| `thermal.throttle.now` | object | Conditions active right now. |
| `thermal.throttle.since_boot` | object | Conditions that have occurred at least once since boot. |
| `disk` | object \| null | |
| `disk.devices[]` | object[] \| null | Physical block devices. Rates are per second since the previous round; `busy_percent` is the share of that time with I/O in flight. `written_bytes` is cumulative since boot (useful for SD card wear). |
| `disk.filesystems[]` | object[] | Usage of mounted filesystems. `avail_bytes` is what unprivileged users can still write. |
| `network` | object[] \| null | One entry per interface; loopback and Docker `veth*` pairs are excluded. Rates are per second since the previous round; error and drop counters are cumulative since boot. |
| `errors` | object | Present only when at least one collector failed this round. Maps collector name (`cpu`, `memory`, `thermal`, `disk`, `network`) to its error message. |

### Null fields

A metric is `null` when its collector

- is not available on this machine (for example, piway running on a non-Linux host),
- failed this round (its error is in `errors`), or
- has no baseline yet. Rate-based metrics need two samples, so `cpu`,
  `network` and `disk.devices` are `null` in the very first snapshot after
  startup (`disk.filesystems` is already filled).

Clients must handle `null` for every metric field.

## `GET /api/v1/series`

Recorded history of one metric (docs/STORAGE.md), averaged into buckets.

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `name` | required | A series name. Without labels (`network_receive_bytes_per_second`) it also matches every labelled variant (`…{interface="eth0"}`), so a client can sum or compare them. With labels it matches exactly. |
| `from` | `to` − 1h | RFC 3339 (`2026-10-14T10:00:00Z`), Unix milliseconds, or relative to now (`-24h`). |
| `to` | now | Same formats. The range is half-open: `[from, to)`. |
| `step` | range / 300 | Bucket size as a duration (`5m`). Raised so no series has more than 1000 points, including partial boundary buckets, and rounded up to the resolution read. |

The server picks the resolution:

| `resolution` | Used when |
| ------------ | --------- |
| `raw` | `step` < 1 min, the range is at most 6 h, and raw data reaches back to `from` |
| `1m` | `step` < 1 h and per-minute rollups reach back to `from` |
| `1h` | longer steps, or older ranges |

```json
{
  "resolution": "1m",
  "step_seconds": 300,
  "from": "2026-10-13T12:00:00Z",
  "to": "2026-10-14T12:00:00Z",
  "series": [
    {
      "name": "cpu_usage_percent",
      "points": [[1791892800000, 12.4, 3.1, 61.0], [1791893100000, 9.8, 2.5, 40.2]]
    }
  ]
}
```

Each point is `[time_ms, avg, min, max]` for the bucket starting at
`time_ms`. Buckets without data are omitted. A series name that matches
nothing gives `"series": []`.

| Status | Meaning |
| ------ | ------- |
| `200` | Result above. `Cache-Control: no-store`. |
| `400` | Missing `name`, unparsable `from`/`to`/`step`, or `to` not after `from`. |
| `503` | Recording is disabled (`data_dir` is empty). |

## `GET /api/v1/alerts`

The alert engine's state ([ALERTS.md](ALERTS.md)).

```json
{
  "active": [
    {
      "rule": "Disk is almost full",
      "series": "filesystem_used_percent{path=\"/\"}",
      "summary": "Disk is almost full (/): 93.2 % (> 90 %)",
      "state": "firing",
      "value": 93.2,
      "since": "2026-10-15T12:05:00Z",
      "fired_at": "2026-10-15T12:05:00Z"
    }
  ],
  "events": [],
  "rules": [{"name": "Disk is almost full", "metric": "filesystem_used_percent", "condition": "> 90", "for": "5m0s", "clear_for": "1m0s"}],
  "deploy_failures": true,
  "app_crashes": true
}
```

- `active`: alerts that are `pending` (breached, waiting for `for`),
  `firing`, or `clearing` (firing, back to normal, waiting for
  `clear_for`); firing first. `since` is when the current state began;
  `fired_at` is set once it fired. `value` is the latest value of a metric
  alert; deploy alerts have none and their `series` is
  `deploy:<repo>@<branch>`.
- `events`: the last 50 announcements, newest first: the alert as it
  fired (`"state": "firing"`) or resolved (`"resolved"`), plus `at`.
- `rules`: the rules in use, built-in or from `alerts_file`.

## Push notifications

Web Push subscriptions ([ALERTS.md](ALERTS.md#push-notifications)). All
need [sign-in](#sign-in).

| Method and path | Response |
| --------------- | -------- |
| `GET /api/v1/push` | `{"public_key": "<VAPID key, base64url>", "subscriptions": [{"id", "label", "service", "created_at", "last_ok_at", "last_error"}…]}`. Endpoints and keys are never returned. |
| `POST /api/v1/push/subscriptions` | Body `{"subscription": PushSubscription.toJSON(), "label": "Chrome on Android"}`. `201` with the subscription; the same endpoint again updates it. `400` for an endpoint that is not https on a known push service, or bad keys; `409` at 20 devices. |
| `DELETE /api/v1/push/subscriptions/{id}` | `204`, or `404`. |
| `POST /api/v1/push/test` | Optional body `{"id": "..."}`. Sends a test notification now (to one device, or all) and answers `{"results": [{"id", "label", "ok", "error"}…]}`. |

## `POST /api/v1/webhooks/github`

Receives GitHub webhook deliveries; see [DEPLOY.md](DEPLOY.md) for setup and
the checks applied. Responses are JSON `{"status": …, "reason": …}`:

| Status | `status` | Meaning |
| ------ | -------- | ------- |
| `202` | `queued` | A push to a configured branch; a deploy was requested. |
| `200` | `pong`, `ignored`, `duplicate` | Ping, a push or event that does not deploy, or a delivery already handled. |
| `400` | `rejected` | Unreadable payload. |
| `401` | `rejected` | Missing or invalid signature. |
| `413` | `rejected` | Body over 5 MiB. |
| `503` | `disabled`, `error` | No `webhook_secret` set, or the deploy could not be queued. A queue failure leaves the delivery ID eligible for redelivery. |

## Builds

Deploys started by webhooks ([DEPLOY.md](DEPLOY.md)). All answer `503` when
deploys are off (no Docker daemon).

| Method and path | Response |
| --------------- | -------- |
| `GET /api/v1/builds` | `{"builds": [Build…]}`, newest first, at most 50. |
| `GET /api/v1/builds/{id}` | One `Build`, or `404`. |
| `GET /api/v1/builds/{id}/log` | The log so far, `text/plain`. |
| `GET /api/v1/builds/{id}/log/stream` | WebSocket: the log so far, then new output as it is written, as text messages (not aligned to lines). Closes with `1000` when the build ends; at once for a finished or still-queued build. |
| `POST /api/v1/builds/{id}/cancel` | `202 {"status":"cancelling"}`; `409` if it already finished; `404`. |
| `GET /api/v1/apps` | `{"apps": [App…]}`, by name: every app that has been built. |
| `POST /api/v1/apps/{name}/redeploy` | `202` with the queued `Build`: a fresh build of the commit that is live now (`"pusher":"dashboard"`); `404` if the app has no successful build. |
| `POST /api/v1/apps/{name}/deploy` | Optional body `{"commit": "<full SHA>"}`. `202` with the queued `Build` (`"pusher":"api"`): the newest commit of the app's branch (asked of the remote with `git ls-remote`), or that commit. `404` if the app was never built; `400` for a commit that is not a full SHA; `502` if the remote cannot be asked. |
| `POST /api/v1/deploys` | Body `{"repo": "owner/name", "branch": "main", "path": optional app directory, "commit": optional}`: the same for a repository branch in `apps.yaml`, deployed before or not (cloned from GitHub unless a push gave another URL). `404` if it is not in `apps.yaml`. |
| `GET /api/v1/targets` | `{"targets": [{"repo", "branch", "app"}…]}`: the branches in `apps.yaml`; `app` once one was built. |
| `POST /api/v1/apps/{name}/restart` | `202` with the queued `Build` (`"kind":"restart"`): a fresh container of the live version, health-checked and switched to without downtime, no rebuild. `404` if the app never ran. |
| `GET /api/v1/apps/{name}/env` | The live version's environment: `{"app", "build", "static", "env": [{"name", "value"}], "secrets": [{"name", "set", "declared", "value"}], "injected": [{"name", "from", "set", "value"}], "restart_needed", "file", "values_shown"}`. `env` is `run.env`; `secrets` are `run.secrets`, then names in the secrets file that piway.yaml does not declare (`declared: false`); `injected` are variables piway sets itself, such as `DATABASE_URL` for `run.databases` (`from` names the engine; `set` is false until the first deploy sets it up). `value` (of a secret or an injected variable) is present only for a signed-in browser session (`values_shown`), never with an API token or through MCP. `restart_needed` means the running container started with other secret values. `Cache-Control: no-store`. `404` if the app never ran. |
| `PUT /api/v1/apps/{name}/secrets/{secret}` | Body `{"value": "…"}`: stores the secret's value, which the app gets on its next restart or deploy. Answers with the environment, as above. `400` for a bad name or a value with a line break; `403` with an API token or through MCP (browser sessions only); `404` for an unknown app. |
| `DELETE /api/v1/apps/{name}/secrets/{secret}` | Deletes the value. Same answers and rules as `PUT`. |
| `GET /api/v1/apps/{name}/volumes` | `{"volumes": [{"name", "path", "docker", "declared", "created", "size_bytes", "in_use"}]}`: the volumes the live version declares (`run.volumes`), then ones left from earlier versions (`declared: false`, no `path`). `created` is false until a release makes the volume; `size_bytes` is `-1` when unknown. `404` if the app never ran. |
| `DELETE /api/v1/apps/{name}/volumes/{volume}` | Deletes a volume and its data, for good. `204`; `409` if the live version declares it or a container uses it; `404` if there is no such volume. |
| `GET /api/v1/apps/{name}/logs` | `?lines=` (1 to 5000, default 100). The last lines of the running container's stdout and stderr, with timestamps, as `text/plain`. `404` for an unknown app; `409` for a static site or when no container runs. |
| `POST /api/v1/apps/{name}/rollback` | `202` with the queued rollback `Build` (`"kind":"rollback"`, `"rollback_of"`); `409` if the app has no earlier successful build; `404` if it was never deployed. |

```json
{
  "id": "20261014T185759Z-b691d31",
  "app": "guestbook",
  "repo": "OFK0/guestbook",
  "branch": "main",
  "commit": "b691d31695bad228fc1ac6499c47624d8b786337",
  "pusher": "ofk0",
  "clone_url": "https://github.com/OFK0/guestbook.git",
  "state": "succeeded",
  "image": "piway/guestbook:b691d31695ba",
  "queued_at": "2026-10-14T18:57:59Z",
  "started_at": "2026-10-14T18:57:59Z",
  "finished_at": "2026-10-14T18:58:11Z"
}
```

`kind` is `"rollback"` for a rollback, `"restart"` for a restart, and
absent for a build; `pusher` is the GitHub user for a push, `dashboard`
for a redeploy and `api` for a deploy through the API or MCP.
`state` is `queued`, `running`, `succeeded`, `failed` or `cancelled`;
`error` explains the last two. `app` and `image` appear once known. A
static site has no `image`; its `site` is the id of the build whose copy
of the site it serves (its own, or for a rollback the earlier one).

An `App`:

```json
{
  "name": "guestbook",
  "repo": "OFK0/guestbook",
  "branch": "main",
  "port": 23438,
  "url": "https://guestbook.pi.local:8443",
  "live": {"id": "20261014T185759Z-b691d31", "state": "succeeded", "…": "…"},
  "last": {"id": "20261014T185759Z-b691d31", "state": "succeeded", "…": "…"},
  "container": {"name": "piway-guestbook-20261014t185759z-b691d31", "state": "running", "image": "piway/guestbook:b691d31695ba", "started_at": "2026-10-14T18:58:09Z"}
}
```

`port` is the app's stable `127.0.0.1` address. `url` is where users reach
it, present only with `proxy_domain` set ([PROXY.md](PROXY.md)). `live`
is the `Build` serving now (its latest successful build or rollback),
`last` the latest of any outcome; `container` is absent when none runs.

## Repositories

The repositories that deploy on push, as listed in `apps.yaml`. Changes are
written to that file (comments and ordering kept) and apply at once, without a
restart; editing the file by hand is picked up the same way. These work even
when deploys are off.

| Endpoint | Meaning |
|---|---|
| `GET /api/v1/repos` | `{"repos": [{"repo", "branch", "path", "app", "auth", "public_key", "deploy_key_url"}…], "webhook": {"path", "configured", "url", "url_source"}, "private": {"available", "token_set"}}`. `app` is set once the branch was built; `configured` says whether `webhook_secret` is set. `url` is the address GitHub reaches the webhook endpoint at, from `webhook_url` or Tailscale Funnel (`url_source` says which), and empty when piway knows none: not the address the dashboard was opened at. `auth` is `deploy-key` or `token`, absent for a public repository; a deploy key comes with its public key and the GitHub page to add it on. |
| `POST /api/v1/repos` | Body `{"repo": "owner/name", "branch": "main", "path": "apps/web", "auth": "deploy-key"}` (branch defaults to `main`, path to the repository root, auth to public). `201` with the entry, including the new deploy key's `public_key`; `400` for a bad name or auth, or an auth that differs from the repository's other branches; `409` if already listed. |
| `DELETE /api/v1/repos?repo=owner/name&branch=main&path=apps/web` | Stops deploying. Without `branch`, every branch of the repository goes; without `path`, every app of the branch (`path=` with nothing is the root's only). `404` if not listed. Running apps and their data are untouched. When the last branch goes, so does its deploy key: `{"removed": […], "key_removed": true}`. |
| `POST /api/v1/repos/test` | Body `{"repo": "owner/name"}`: can piway read it (`git ls-remote`)? `{"ok": true}`, or `{"ok": false, "kind", "message", "detail"}` where `kind` is `no_key`, `key_rejected`, `not_found`, `no_token`, `token_rejected`, `host_key`, `network` or `other`, `message` says what to do and `detail` is git's own line. `404` if not listed. |
| `PUT /api/v1/repos/token` | Body `{"token": "github_pat_…"}`: stores the token for `auth: token` repositories. `204`. It is never returned. |
| `DELETE /api/v1/repos/token` | Deletes the token. `204`. |

## Databases

The shared database engines ([DATABASES.md](DATABASES.md)). Both answer
`503` without Docker.

| Endpoint | Meaning |
|---|---|
| `GET /api/v1/databases` | `{"engines": [{"name", "state", "image", "pinned", "upgrade_available", "volume", "size_bytes", "memory_bytes", "memory_limit_bytes", "apps": [{"name", "backups", "last_backup", "last_backup_bytes"}]}]}`, one per engine. `state` is `not_started` until an app asks for the engine, else the container's state. `image` is what runs, `pinned` what new servers start from; `upgrade_available` when their major versions differ (`piway db upgrade`). Sizes are `-1` when unknown. |
| `POST /api/v1/databases/backup` | Backs up every app's database on every running engine now, as the nightly job does. `{"done": ["postgres/blog: <file> (<bytes> bytes)", …]}`; `500` with `error` and what was `done` if any failed. |

## `GET /api/v1/stream` (WebSocket)

A one-way live stream of snapshots. Each text message is exactly the body of
`GET /api/v1/snapshot` (same schema, same `null` rules), sent once per
collection round. Messages the client sends are ignored.

```js
const ws = new WebSocket(`ws://${location.host}/api/v1/stream`);
ws.onmessage = (e) => render(JSON.parse(e.data));
```

- **First message:** if a round has already completed, the latest snapshot is
  sent immediately on connect, so a client never waits a full interval for
  data.
- **Gaps:** `seq` increases by 1 per round. A jump means the client missed
  rounds (for example, after reconnecting).
- **Keepalive:** the server pings every 15 s. A client that does not answer
  within 5 s is disconnected. Browsers answer pings automatically.
- **Sign-in and origin:** the upgrade needs a session like any API call
  (the browser sends the cookie), and cross-origin upgrades are rejected
  (`403`).

### Close codes

| Code | Reason | Meaning | Client should |
| ---- | ------ | ------- | ------------- |
| `1001` Going Away | `server shutting down` | piway is stopping or restarting. | Reconnect with backoff. |
| `1013` Try Again Later | `too slow` | The client fell 8 snapshots behind (slow network or a stalled tab) and was dropped so it could not hold back other clients. | Reconnect with backoff. |

## `GET /metrics`

The newest snapshot in the [Prometheus text format](https://prometheus.io/docs/instrumenting/exposition_formats/)
(`Content-Type: text/plain; version=0.0.4`), for Prometheus, Grafana Agent
or any compatible scraper. The output passes `promtool check metrics`.

Like the API, `/metrics` needs [sign-in](#sign-in): give Prometheus an
[API token](#api-tokens).

```yaml
# prometheus.yml
scrape_configs:
  - job_name: piway
    static_configs:
      - targets: ["<pi-address>:8080"]
    authorization:
      credentials_file: /etc/prometheus/piway-token # the piway_… token
```

Values use base units (seconds, bytes, degrees Celsius). Unlike the JSON API,
CPU, disk and network are cumulative counters, not rates: use `rate()` over
them, so no activity between scrapes is lost at any scrape interval.

| Metric | Type | Labels | Meaning |
| ------ | ---- | ------ | ------- |
| `piway_build_info` | gauge | `version`, `commit`, `goversion` | Always 1. |
| `piway_snapshot_timestamp_seconds` | gauge | | When the newest snapshot was collected. `time() - piway_snapshot_timestamp_seconds` shows a stalled agent. |
| `piway_cpu_seconds_total` | counter | `cpu`, `mode` | Time per core in `user`, `nice`, `system`, `idle`, `iowait`, `irq`, `softirq`, `steal`. |
| `piway_memory_{total,available,free,buffers,cached}_bytes` | gauge | | From `/proc/meminfo`. Used is `total - available`. |
| `piway_memory_swap_{total,free}_bytes` | gauge | | Swap size and unused swap. |
| `piway_cpu_temperature_celsius` | gauge | | SoC temperature. |
| `piway_throttle_active` | gauge | `condition` | 1 while the firmware reports `under_voltage`, `freq_capped`, `throttled` or `soft_temp_limit`. |
| `piway_throttle_occurred` | gauge | `condition` | 1 if the condition has happened since boot. |
| `piway_disk_{read,written}_bytes_total` | counter | `device` | Bytes read and written by each physical disk. |
| `piway_disk_{reads,writes}_completed_total` | counter | `device` | Requests completed. |
| `piway_disk_io_time_seconds_total` | counter | `device` | Time with at least one request in flight; `rate()` of it is the busy fraction. |
| `piway_filesystem_{size,used,avail}_bytes` | gauge | `mountpoint` | Filesystem space. |
| `piway_network_{receive,transmit}_{bytes,packets,errors,drop}_total` | counter | `interface` | Per interface, excluding loopback and Docker `veth*`. |

- A metric whose collector is unavailable or failed this round is left out,
  as `null` is in the JSON API. Before the first round, only
  `piway_build_info` is reported.
- Series appear and disappear with their device or interface.

## Dashboard

piway serves the dashboard (`web/`) from the binary when it was built with
`make build` (Go build tag `embedui`). Any path not listed above serves it:

| Path | Response | `Cache-Control` |
| ---- | -------- | --------------- |
| `/assets/*` | The file. Names contain a content hash, so a new build gets new URLs. | `public, max-age=31536000, immutable` |
| Other existing files (`/favicon.svg`) | The file. | `no-cache` |
| Any other path without an extension (`/`, `/design-system`) | `index.html`, so the client-side router can render the route. | `no-cache` |
| A missing path with an extension (`/assets/old.js`) | `404`, never `index.html`. | |
| An unknown path under `/api/` | `404` with the JSON error body. | |

- Every file has a strong `ETag`. `no-cache` files are revalidated with
  `If-None-Match` and usually answered with `304 Not Modified`.
- Text files (`.js`, `.css`, `.html`, `.svg`) are gzip-compressed once at
  startup and sent compressed to clients that accept it, with
  `Vary: Accept-Encoding` and a separate `ETag`.
- All dashboard responses carry `X-Content-Type-Options: nosniff`.
- A binary built without the dashboard (`make build-go`, `go run`) answers
  these paths with a short `404` page that explains how to build it. The API
  works either way.
