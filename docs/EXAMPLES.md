# Example apps

The [`examples/`](https://github.com/OFK0/piway/tree/main/examples)
directory has apps that do real work, deployed by piway like your own:
each has a `piway.yaml` and a Dockerfile, and is added with its directory:

```sh
piway repo add OFK0/piway -path examples/loadlab
```

| App | Language | What it exercises | Image | Idle memory |
| --- | -------- | ----------------- | ----- | ----------- |
| [loadlab](https://github.com/OFK0/piway/tree/main/examples/loadlab) | Go | CPU, memory and disk on demand; limits, OOM kills, volumes; a WebSocket stream | 9.4 MB | 2 MiB |
| [thumbnailer](https://github.com/OFK0/piway/tree/main/examples/thumbnailer) | Python | Uploads, a background worker making thumbnails, progress over a WebSocket, expiring files | 295 MB | 35 MiB |
| [chat](https://github.com/OFK0/piway/tree/main/examples/chat) | Node.js | Rooms over WebSocket, Redis pub/sub and history (`run.databases`), attachments | 247 MB | 36 MiB |
| [notes](https://github.com/OFK0/piway/tree/main/examples/notes) | PHP | CRUD on MariaDB, sessions in Redis, attachments, a scheduled cleanup script | 732 MB | 13 MiB |
| [feeds](https://github.com/OFK0/piway/tree/main/examples/feeds) | Ruby | Scheduled RSS fetching, SQLite on a volume (WAL), new items over SSE, OPML | 299 MB | 53 MiB |
| [reports](https://github.com/OFK0/piway/tree/main/examples/reports) | Java | Queued CSV and PDF reports on a thread pool, MariaDB, progress over SSE, a JVM inside `run.memory` | 537 MB | 112 MiB |
| [events](https://github.com/OFK0/piway/tree/main/examples/events) | Clojure | Event ingestion, time-window aggregation with core.async, stats over a WebSocket, state handed over across a deploy | 503 MB | 52 MiB |

Each app's README has what it does, how it uses piway and what it measured
on a Raspberry Pi 4. They share a [common contract](https://github.com/OFK0/piway/tree/main/examples#common-contract):
`/healthz`, `/api/info`, a clean shutdown on SIGTERM and logs on standard
output.
