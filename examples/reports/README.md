# reports (Java)

Report requests go into a queue in MariaDB; two workers on a thread pool
turn them into CSV (every one of 100,000 sample sales) or PDF (monthly
totals per region, with OpenPDF); the page follows each one over
Server-Sent Events, and the files are kept on the data volume. Spring Boot
3.5 on Java 21.

| Endpoint | What it does |
| -------- | ------------ |
| `POST /api/reports` `{"kind": "csv" \| "pdf"}` | Queues a report; `202` with it. |
| `GET /api/reports`, `GET /api/reports/{id}` | The newest 50 and their state, progress and row count. |
| `GET /events` | Every report change (SSE). |
| `GET /files/{id}` | A finished report. The newest 50 files are kept. |
| `GET /` | A page to request reports and watch them. |
| `GET /api/info`, `GET /healthz` | The [common contract](../README.md#common-contract); `/api/info` also shows the JVM's heap limit, `/healthz` queries MariaDB. |

What it shows about piway:

- `run.databases: [mariadb]`: the app turns `DATABASE_URL` into a JDBC URL
  itself, makes its tables with `CREATE TABLE IF NOT EXISTS` (additive) and
  fills the sample data once.
- A JVM inside `run.memory`: it sizes its heap from the container's limit
  (`-XX:MaxRAMPercentage=60`, so 224 MiB of 384 MiB), with SerialGC and C1
  only, the lean choices for a small heap on few cores. `run.health.start_period`
  gives it two minutes to start.
- Work that outlives a version: on SIGTERM Spring stops taking requests,
  closes the SSE streams and lets the reports in progress finish (up to
  20 s). Reports are claimed in the database (`UPDATE … WHERE state =
  'queued'`), so queued ones are run once, by whichever version gets them.
- A two-stage Dockerfile: Maven with the dependencies in their own cached
  layer, then a JRE image.

## Deploy

```sh
piway repo add OFK0/piway -path examples/reports
```

Set its secret `REPORTS_TOKEN` on the app's page (any value) and deploy.

## Measured on a Raspberry Pi 4 (4 GB, SD card)

| | |
| --- | --- |
| First deploy | about 7 min from nothing: the Maven and JRE images, Maven's dependencies, MariaDB, and 100,000 sample rows (4 s) |
| Redeploy with a warm cache | 75 s, most of it Maven and the JVM starting (11 s) |
| Memory while building | the Pi's used memory rose by about 200 MiB; Maven did not come near running out |
| Image | 537 MB |
| Memory, idle | 112 MiB of 384 MiB (heap limit 224 MiB) |
| CSV of 100,000 rows (4.1 MB) | 5.7 s, 36 progress events; memory peaking at 135 MiB |
| PDF of monthly totals | 2.7 s |
| Three reports queued, then a restart | all three finished, by the old version during its graceful stop |

Found on the way, in the app: Spring's `SseEmitter` sends the response
headers only with its first event, so a client waited (and timed out)
until a report changed; the stream now starts with a comment. And Maven's
`dependency:go-offline` misses some artifacts, so the package step cannot
run offline.
