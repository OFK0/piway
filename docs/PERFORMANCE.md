# Performance

What piway costs on a Raspberry Pi 4 and how a change is kept from making it
worse. Figures are from a Pi 4 (4 GB, arm64) unless said otherwise.

## Storage: 30 days of history

`TestSimulateDays` (`internal/history/simulate_test.go`) records history with
a fake clock: 60 series, one round a second, compaction every 10 s, default
retention (24 h raw, 7 d of minutes, 365 d of hours). The values are random
walks, so they are no easier to store than real ones.

```sh
PIWAY_SIMULATE_DAYS=30 go test ./internal/history -run SimulateDays -v -timeout 0   # about 17 minutes
```

| After | raw | 1m | 1h |
| ----- | --- | -- | -- |
| 1 day | 48.1 MB | 3.3 MB | 0.1 MB |
| 8 days | 50.1 MB | 23.1 MB | 0.4 MB |
| 30 days | 50.1 MB | 23.1 MB | 1.6 MB |

- **Disk use** stops growing for the raw and minute tiers once retention
  starts to delete (day 2 and day 8). The hourly tier grows by about
  55 KB a day, so a year is about 20 MB: roughly 95 MB in all.
- **Compaction** took 2.9 ms on average over 259,200 runs. The slowest run
  took 2.1 s; what made it slow was not looked into.
- **Written**: 2.15 MB per simulated hour from the history's own files. This
  is the data; the card also sees ext4 journal traffic, measured in
  [STORAGE.md](STORAGE.md#measured-bytes-written-per-hour).

The simulation does not use the real clock, so `flush_interval` does not
matter in it: a run measures how much data is written, not how often.

## Load

A piway started apart from the real one (see [DEVELOPMENT.md](DEVELOPMENT.md#run-your-build)),
on a history of two days made by `TestSimulateDays` (60 series). The load
generators run on the same Pi, so they take CPU from piway: the latencies
are worse than a client on another machine would see.

```sh
PIWAY_SIMULATE_DAYS=2 PIWAY_SIMULATE_DIR=/tmp/piway-load go test ./internal/history -run SimulateDays -timeout 0
go run ./cmd/wsload -url ws://127.0.0.1:18080/api/v1/stream -clients 1000 -slow 20 -duration 30s
go run ./cmd/apiload -url 'http://127.0.0.1:18080/api/v1/series?name=cpu_usage_percent&from=-24h' -workers 50 -duration 20s
```

Piway's own figures come from `/metrics` (`piway_process_*`).

| Load | Result | piway |
| ---- | ------ | ----- |
| Idle | | 15 MB RSS, 17 goroutines, 22 descriptors |
| 1000 WebSocket clients + 20 that never read, 30 s | 0 failed; every client got 30 or 31 snapshots in 30 s; lag p50 43 ms, p99 533 ms | 65 MB RSS, 2056 goroutines, 1040 descriptors, 0.15 CPU-seconds per second (4.4 s in 30 s) |
| After the clients leave | | 66 MB RSS, 18 goroutines, 21 descriptors: nothing is left behind |
| 50 workers asking for 24 h (`1m` tier) | 0 failed; 59 requests/s; p50 0.80 s, p99 1.6 s. One request alone takes 58 ms | 357 MB RSS at the peak, 144 MB 3 s later; CPU-bound on the four cores |
| 50 workers asking for 1 h (raw) | 0 failed; 93 requests/s; p50 0.50 s, p99 1.4 s | |
| 50 workers asking for 48 h in 1 h steps (`1h` tier) | 0 failed; 754 requests/s; p50 43 ms | |

## Daemon restarts

`TestDaemons` (`e2e/daemons_test.go`, `make e2e E2E_RUN='TestDaemons$'`)
restarts what piway depends on while an app runs, on a Pi 4 with rootless
Docker. Both scenarios pass (382 s in all, most of it the app's build and a
redeploy).

- **Caddy**: killed with `kill -9`, then started again with an empty config.
  The app keeps answering on its own address while Caddy is away; piway logs
  "caddy unreachable" once, puts the routes back by itself ("caddy reachable
  again"), and a redeploy after that still works. The test starts its own
  Caddy on its own admin port.
- **Docker**: `systemctl --user restart docker` (off unless
  `E2E_RESTART_DOCKER=1`, since it stops every container on the daemon for a
  moment). Docker restarts the app, piway follows it to its new port, the
  route through Caddy works, piway itself keeps answering, and a redeploy
  works.

## Twenty apps

`TestScale` (`e2e/scale_test.go`, `make e2e E2E_RUN='TestScale$'`) deploys 20
copies of the loadlab example (a Go app, 64 MiB and half a core each) on real
Docker: a signed push webhook for each within 8 seconds, then it waits for
every build and checks every stable address.

| Measure | Result |
| ------- | ------ |
| 20 pushes accepted | 8.2 s (0.4 s each) |
| All 20 built and released | 31 min 33 s, builds one at a time (about 95 s each; loadlab's layers were already cached) |
| Every app healthy on its stable address | yes |
| `GET /api/v1/apps` with 20 apps | p50 216 ms, p99 304 ms |
| piway with no apps | 24 MB RSS, 16 goroutines, 19 descriptors |
| piway with 20 apps | 26 MB RSS, 102 goroutines, 83 descriptors |
| A minute later, at rest | 25 MB RSS, 60 goroutines, 62 descriptors, 0.004 CPU-seconds per second |

- **piway itself barely grows**: 2 MB of RSS and 64 descriptors for 20 apps.
  The goroutines fell from 102 to 60 within a minute; why was not looked into.
- **The wait is the build.** One at a time is the design (a Pi 4 has four
  cores and an SD card); the pushes themselves are quick and none is lost.
  The queue holds 50 branches (`TestBurstOfPushes`).
- **The apps list** asks Docker about every container, so it is the slowest
  call here, and grows with the number of apps. It is fine for a dashboard
  that asks every few seconds; if it matters, the answer is to cache it.

## Soak

`TestSoak` (`e2e/soak_test.go`) keeps an isolated piway busy and reads its
own process every five minutes. It ran three example apps (loadlab,
thumbnailer, docs-site) with 20 dashboard clients on the WebSocket stream, a
history query a second, a request to each app every 250 ms, and a redeploy of
each app every 30 minutes.

**It ran for 22.5 hours of the 24 planned**: the Pi was shut down at that
point. The test never reached its own verdict, so the figures below apply its
rule to the readings it had written (251, the first hour left out as warm-up):
the median of the last quarter may not exceed the first quarter's by more
than 25% of RSS (and 10 MB), 20 goroutines or 20 descriptors.

| Measure | First quarter (median) | Last quarter (median) | Whole run |
| ------- | ---------------------- | --------------------- | --------- |
| RSS | 30.4 MB | 29.6 MB | 26.7 to 33.1 MB, trend -0.02 MB/h |
| Goroutines | 68 | 68 | 68 to 74 |
| Descriptors | 50 | 50 | 50 to 54 |

- **Flat**: nothing grew. The highs are the moments around a redeploy.
- **All three apps answered at every reading.**
- **CPU**: 0.073 cores on average, with 41,141 garbage collection cycles in
  22.5 hours.
- A soak of 24 to 72 hours was the aim; this is shorter, and a longer run
  would need a machine that stays on.

## API examples

`internal/server/apidocs_test.go` reads the JSON examples out of
[API.md](API.md) (`/healthz`, snapshot, series, alerts, `Build`, `App`) and
compares their fields and types with what the code sends. A field renamed,
retyped, added or removed in only one of the two fails `make test`.

## Bursts and chaos

`TestBurstOfPushes` (`internal/deploy`) pushes 800 times to 80 branches
while a build runs: the queue stays within its bound of 50, pushes that do
not fit are refused with an error, and nothing is left queued or running.
It found that two pushes in the same second could freeze the pipeline (#156).

Two end-to-end scenarios ([e2e/README.md](../e2e/README.md)) break things on
real Docker, and both pass on the Pi:

- `kill9-build`: `kill -9` of piway during a build. The app keeps serving, the
  build is marked failed as interrupted, and the next piway deploys.
- `container-killed`: an OOM kill of an app. Docker restarts it, the address
  follows, and the "App crashed" alert is raised.

### Full disk

`TestFullDisk` in `internal/tsdb`, `internal/history` and `internal/deploy`
runs on a real 256 to 512 KB tmpfs (`internal/testfs`: the test binary starts
again in a user and mount namespace, so no root is needed) and fills it, so
the kernel answers ENOSPC:

- **History**: `Append` returns the error and the records written before it
  stay readable; a failed `Compact` loses no raw data and, once space is back,
  rolls up the same minutes without duplicates; the store reopens cleanly.
- **Builds**: a push that cannot be recorded is refused with the reason and
  leaves no half-written record. A running build whose log cannot be written
  goes on: it used to get the write error, which stops a `docker build`'s
  output from being read and can block it (#159).


## Allocations

`scripts/bench-allocs.sh BASE_REF` runs every benchmark on `BASE_REF` and on
the working tree and fails when a benchmark allocates more per operation
(more than 10% and at least 2). CI runs it on each pull request against its
base branch. Allocation counts are the same on every machine, so a shared
runner can compare them; run times are not, so they are not compared.

```sh
scripts/bench-allocs.sh origin/main
```
