# Example apps

Apps that do real work, deployed by piway exactly like your own: each is a
directory here with a `piway.yaml` and a Dockerfile, added with
`piway repo add OFK0/piway -path examples/<app>`. They are documentation and
the fixtures of piway's end-to-end tests (#113).

| App | Language | What it exercises |
| --- | -------- | ----------------- |
| [loadlab](loadlab) | Go | CPU, memory and disk on demand; limits, OOM kills, volumes; a WebSocket stream |
| [thumbnailer](thumbnailer) | Python | Uploads, a background worker making thumbnails, progress over a WebSocket, expiring files |
| [chat](chat) | Node.js | Rooms over WebSocket, Redis pub/sub and history (`run.databases`), attachments |
| [notes](notes) | PHP | CRUD on MariaDB, sessions in Redis, attachments, a scheduled cleanup script |
| [feeds](feeds) | Ruby | Scheduled RSS fetching, SQLite on a volume (WAL), new items over SSE, OPML |
| [reports](reports) | Java | Queued CSV and PDF reports on a thread pool, MariaDB, progress over SSE, a JVM inside `run.memory` |
| [events](events) | Clojure | Event ingestion, time-window aggregation with core.async, stats over a WebSocket, state handed over across a deploy |

More are on the way (#112): a static docs site.

## Common contract

Every example app:

- builds natively on the Pi (linux/arm64) from its Dockerfile;
- serves `GET /healthz` (its `run.health.path`);
- serves `GET /api/info`: its language and version, and proof that its
  environment and a secret arrived (the secret's length and a hash prefix,
  never its value);
- shuts down cleanly on SIGTERM, which piway sends when it replaces a
  version;
- logs to standard output, which the app's page shows;
- stays within its `run.memory`, with its build time, image size and memory
  use measured on a Pi 4 in its README.
