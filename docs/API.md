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
| `step` | range / 300 | Bucket size as a duration (`5m`). Raised so no series has more than 1000 points, and to at least the resolution read. |

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
| `503` | `disabled`, `error` | No `webhook_secret` set, or the deploy could not be queued. |

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

`kind` is `"rollback"` for a rollback (absent for a push).
`state` is `queued`, `running`, `succeeded`, `failed` or `cancelled`;
`error` explains the last two. `app` and `image` appear once known.

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
- **Origin:** cross-origin upgrades are rejected (`403`), since the endpoint
  has no authentication yet.

### Close codes

| Code | Reason | Meaning | Client should |
| ---- | ------ | ------- | ------------- |
| `1001` Going Away | `server shutting down` | piway is stopping or restarting. | Reconnect with backoff. |
| `1013` Try Again Later | `too slow` | The client fell 8 snapshots behind (slow network or a stalled tab) and was dropped so it could not hold back other clients. | Reconnect with backoff. |

## `GET /metrics`

The newest snapshot in the [Prometheus text format](https://prometheus.io/docs/instrumenting/exposition_formats/)
(`Content-Type: text/plain; version=0.0.4`), for Prometheus, Grafana Agent
or any compatible scraper. The output passes `promtool check metrics`.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: piway
    static_configs:
      - targets: ["<pi-address>:8080"]
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
