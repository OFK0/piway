// The example apps, as examples/README.md lists them.
export const apps = [
  { name: 'loadlab', language: 'Go', what: 'CPU, memory and disk on demand; limits, OOM kills, volumes; a WebSocket stream' },
  { name: 'thumbnailer', language: 'Python', what: 'Uploads, a background worker making thumbnails, progress over a WebSocket, expiring files' },
  { name: 'chat', language: 'Node.js', what: 'Rooms over WebSocket, Redis pub/sub and history, attachments' },
  { name: 'notes', language: 'PHP', what: 'CRUD on MariaDB, sessions in Redis, attachments, a scheduled cleanup script' },
  { name: 'feeds', language: 'Ruby', what: 'Scheduled RSS fetching, SQLite on a volume, new items over SSE, OPML' },
  { name: 'reports', language: 'Java', what: 'Queued CSV and PDF reports on a thread pool, MariaDB, progress over SSE' },
  { name: 'events', language: 'Clojure', what: 'Event ingestion, time-window aggregation with core.async, state handed over across a deploy' },
  { name: 'docs-site', language: 'Static (Vite)', what: 'This site: built in a container on deploy, served as files, client-side routes' },
]
