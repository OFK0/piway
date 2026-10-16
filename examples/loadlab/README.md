# loadlab (Go)

Puts load on the Pi on purpose, to see what piway does with it: CPU,
memory and disk on demand, and a live view of the container's resources.
A fixture of piway's end-to-end tests, too.

| Endpoint | What it does |
| -------- | ------------ |
| `POST /cpu?workers=2&seconds=10` | Busy loops on that many goroutines. |
| `POST /mem?mb=100&seconds=30` | Allocates and touches that much memory, holds it, then frees it. Past `run.memory` the kernel kills the app (an OOM kill). |
| `POST /disk?mb=50` / `DELETE /disk` | Writes files to the data volume, or deletes them. |
| `GET /ws` | The process's and the container's memory, the limit, CPU workers and disk use, every second (WebSocket); `GET /api/stats` once. |
| `GET /` | A page with buttons for all of the above and the live numbers. |
| `GET /api/info`, `GET /healthz` | The [common contract](../README.md#common-contract). |

What it shows about piway:

- `run.cpus: 1` caps it at one core: two workers together use 100%, not 200%.
- `run.memory: 128MiB` is enforced where the memory cgroup is on: `/mem?mb=200`
  gets the app OOM-killed, and Docker restarts it.
- The data volume is writable by the app's non-root user (65532): a new volume
  starts as a copy of the image's `/data`, owner included.
- It runs from a `scratch` image: the binary and nothing else.

## Deploy

It deploys from this repository's `examples/loadlab` directory:

```sh
piway repo add OFK0/piway -path examples/loadlab
```

Then set its secret `LOADLAB_TOKEN` on the app's page (any value; `/api/info`
shows its length and a hash prefix, never the value) and deploy.

## Measured on a Raspberry Pi 4 (4 GB, SD card)

| | |
| --- | --- |
| First build | 2 min 4 s, including pulling `golang:1.27-alpine` |
| Build with a warm cache | 8 s |
| Image | 9.4 MB |
| Memory, idle | about 2 MiB of the 128 MiB limit |

Found on the way: after an OOM kill Docker restarts the container on a new
host port, and piway's route kept the old one, so the app stayed
unreachable until its next deploy (#132, fixed: piway now follows the
restart within a second and raises an "App crashed" alert).

The Pi has swap (zram), and Docker lets a container swap as much as its
`run.memory`, so with 128 MiB it takes more than 256 MiB in all, such as
`/mem?mb=400`, to get an OOM kill.
