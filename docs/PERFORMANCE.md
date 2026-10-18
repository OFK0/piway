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
  took 2.1 s, which is the first run after the clock jumps by a minute
  boundary and an hour boundary together plus a retention delete.
- **Written**: 2.15 MB per simulated hour from the history's own files. This
  is the data; the card also sees ext4 journal traffic, measured in
  [STORAGE.md](STORAGE.md#measured-bytes-written-per-hour).

The simulation does not use the real clock, so `flush_interval` does not
matter in it: a run measures how much data is written, not how often.

## Allocations

`scripts/bench-allocs.sh BASE_REF` runs every benchmark on `BASE_REF` and on
the working tree and fails when a benchmark allocates more per operation
(more than 10% and at least 2). CI runs it on each pull request against its
base branch. Allocation counts are the same on every machine, so a shared
runner can compare them; run times are not, so they are not compared.

```sh
scripts/bench-allocs.sh origin/main
```
