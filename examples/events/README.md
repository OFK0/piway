# events (Clojure)

Takes in events over HTTP, aggregates them in 10-second, 1-minute and
5-minute windows with core.async, streams the statistics over a WebSocket
every second, and snapshots the totals to disk as EDN, so a new version
carries on counting where the old one stopped. http-kit, an
ahead-of-time compiled uberjar built with tools.build.

| Endpoint | What it does |
| -------- | ------------ |
| `POST /api/events` | `{"type": "click", "value": 1.5}` or an array of up to 10,000; `202`, or `503` when the buffer is full. |
| `POST /api/generate?rate=200&seconds=10` | Synthetic events of a few types, for a demo or a load test (up to 5,000 a second). |
| `GET /api/stats` | Per window: events, events per second, and per type the count and the mean, minimum and maximum value; and the totals. |
| `GET /ws` | The same, every second. |
| `GET /` | A live page. |
| `GET /api/info`, `GET /healthz` | The [common contract](../README.md#common-contract). |

What it shows about piway:

- **State handed over during a deploy.** piway starts the new version while
  the old one still runs, and stops the old one only after switching
  traffic. A snapshot the new version reads at start is therefore stale:
  the old one keeps counting until it stops. So each snapshot says who
  wrote it and whether it is the writer's final one (written on SIGTERM).
  A new version that finds a snapshot that is not final takes it, writes
  none of its own (it would overwrite the old one's), and when the old
  version's final snapshot appears, takes that as its base and adds its own
  count on top. Measured: 25,603 events before a redeploy under load, plus
  12,000 generated across it, gave 37,603 after, none lost. The first
  version of this app took the stale snapshot and lost 2,000 events in the
  same test.
- Backpressure: events go through a 10,000-event buffered channel; a full
  buffer answers `503` rather than growing memory.
- A JVM inside `run.memory` (60% heap, SerialGC, C1 only), as in
  [reports](../reports).

## Deploy

```sh
piway repo add OFK0/piway -path examples/events
```

Set its secret `EVENTS_TOKEN` on the app's page (any value) and deploy.

## Measured on a Raspberry Pi 4 (4 GB, SD card)

| | |
| --- | --- |
| First deploy | 5 min 8 s: the Clojure and JRE images, the dependencies, ahead-of-time compiling |
| Redeploy with a warm cache | 19 s |
| Image | 503 MB |
| Memory, idle | 52 MiB of 256 MiB |
| 2,000 events a second | 81 MiB, 41% of a core |
