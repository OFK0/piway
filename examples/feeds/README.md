# feeds (Ruby)

Follows RSS and Atom feeds: fetches them every ten minutes, keeps the
items in SQLite on the data volume, and streams new ones to the page over
Server-Sent Events. Feeds come and go as OPML. Sinatra on Puma.

| Endpoint | What it does |
| -------- | ------------ |
| `GET /api/feeds`, `POST /api/feeds` `{"url"}`, `DELETE /api/feeds/{id}` | The feeds followed; a new one is fetched at once. |
| `GET /api/items?limit=50` | The newest items of every feed. |
| `POST /api/refresh` | Fetch every feed now. |
| `GET /events` | New items as they arrive (SSE), with a keep-alive comment every 15 s. |
| `GET /opml`, `POST /opml` | Export the feeds; import an OPML document (the body). |
| `GET /` | A page that follows, unfollows and shows items live. |
| `GET /api/info`, `GET /healthz` | The [common contract](../README.md#common-contract); `/healthz` also queries SQLite. |

What it shows about piway:

- SQLite on a volume, as [DEPLOY.md](../../docs/DEPLOY.md#sqlite) recommends:
  WAL mode and a busy timeout, because the old and the new version share
  the file for a few seconds during a deploy; a schema made with
  `CREATE … IF NOT EXISTS`, additive only. Both versions may fetch at
  once; a `UNIQUE` constraint and `INSERT OR IGNORE` store each item once.
- A schedule inside the app (a thread, every `FETCH_EVERY` seconds) and
  outbound requests from the container to the internet.
- Long-lived responses through piway's proxy: SSE streams stay open for as
  long as the page does. On SIGTERM Puma finishes requests in flight and
  cuts streams after 5 s; the page's `EventSource` reconnects by itself, to
  the new version.
- A two-stage Dockerfile: Puma's C extension compiles with
  `build-essential` in the first stage; the image that runs has no compiler.

## Deploy

```sh
piway repo add OFK0/piway -path examples/feeds
```

Set its secret `FEEDS_TOKEN` on the app's page (any value) and deploy.

## Measured on a Raspberry Pi 4 (4 GB, SD card)

| | |
| --- | --- |
| First build | about 3 min: pulling `ruby:3.4-slim`, installing `build-essential`, compiling Puma (`sqlite3` comes precompiled) |
| Build with a warm cache | 20 s |
| Image | 299 MB |
| Memory, idle | 53 MiB of 192 MiB, following two feeds |
| Shutdown on SIGTERM | 0.45 s, exit code 0 |

Found on the way, in the app: a client that posts a body with no
`Content-Type` gets a form one from curl, Rack reads the body to parse it,
and the app found it empty; it rewinds the body first now. And Puma exits 1
after a graceful SIGTERM unless `raise_exception_on_sigterm false`.
