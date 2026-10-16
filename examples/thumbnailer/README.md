# thumbnailer (Python)

Upload an image; a background worker makes 128, 512 and 1024 px thumbnails
of it with Pillow, the page follows each job live over a WebSocket, and
the files expire after an hour. FastAPI and uvicorn, one process.

| Endpoint | What it does |
| -------- | ------------ |
| `POST /api/images` | Multipart `file` (up to 20 MB): saves it to the volume and queues a job; `202` with the job. |
| `GET /api/jobs`, `GET /api/jobs/{id}` | Jobs since the process started: state, progress, thumbnails, time taken. |
| `WS /ws` | Every job change as it happens (queued, running with progress, done or failed, expired). |
| `GET /files/{id}/{name}` | The original or a thumbnail. |
| `GET /` | A page to upload and watch. |
| `GET /api/info`, `GET /healthz` | The [common contract](../README.md#common-contract). |

What it shows about piway:

- Work off the request path: uploads return at once, a worker thread does
  the CPU and memory work, and a WebSocket (through piway's proxy) reports it.
- Files on a volume, owned by an unprivileged user (uid 10001), with their
  own expiry (`EXPIRE_MINUTES`, every minute) so the volume does not grow.
- A clean shutdown: on SIGTERM uvicorn stops taking requests and the worker
  finishes the job in hand.
- A memory bound: Pillow refuses images over 40 megapixels, which keeps a
  job inside `run.memory`.

## Deploy

```sh
piway repo add OFK0/piway -path examples/thumbnailer
```

Set its secret `THUMB_TOKEN` on the app's page (any value) and deploy.

## Measured on a Raspberry Pi 4 (4 GB, SD card)

| | |
| --- | --- |
| First build | 2 min, including pulling `python:3.13-slim` (wheels for aarch64, nothing compiles) |
| Build with a warm cache | 18 s |
| Image | 295 MB |
| Memory, idle | 35 MiB of 256 MiB |
| A 3000×2000 image (18 MB BMP) | 0.7 s for three thumbnails, memory peaking at 96 MiB |
| Shutdown on SIGTERM | 0.85 s, exit code 0 |
