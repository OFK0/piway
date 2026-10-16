# Example apps

The [`examples/`](https://github.com/OFK0/piway/tree/main/examples)
directory has apps that do real work, deployed by piway like your own:
each has a `piway.yaml` and a Dockerfile, and is added with its directory:

```sh
piway repo add OFK0/piway -path examples/loadlab
```

| App | Language | What it exercises | Image | Idle memory |
| --- | -------- | ----------------- | ----- | ----------- |
| [loadlab](https://github.com/OFK0/piway/tree/main/examples/loadlab) | Go | CPU, memory and disk on demand: limits, OOM kills, volumes; a WebSocket stream | 9.4 MB | 2 MiB |
| [thumbnailer](https://github.com/OFK0/piway/tree/main/examples/thumbnailer) | Python | Uploads, a background worker making thumbnails, progress over a WebSocket, expiring files | 295 MB | 35 MiB |

Each app's README has what it does, how it uses piway and what it measured
on a Raspberry Pi 4. They share a [common contract](https://github.com/OFK0/piway/tree/main/examples#common-contract):
`/healthz`, `/api/info`, a clean shutdown on SIGTERM and logs on standard
output.
