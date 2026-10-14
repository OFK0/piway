# HTTP API

All endpoints return JSON (`Content-Type: application/json`). Errors use a
single shape:

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
