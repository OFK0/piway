# Metrics history

piway records every snapshot (one per `interval`, 1 s by default) in an
append-only time series store under `data_dir` (`~/.local/state/piway`).
The format and crash recovery are described in `internal/tsdb/tsdb.go`.

## What is recorded

About 60 series on a Pi 4, named in Prometheus style so the range API and
`/metrics` can expose them as they are:

| Series | Labels |
| ------ | ------ |
| `cpu_usage_percent`, `cpu_core_usage_percent` | `core` |
| `memory_{total,used,available,cached,buffers}_bytes`, `swap_{total,used}_bytes` | |
| `temperature_celsius`, `throttle_{under_voltage,freq_capped,throttled,soft_temp_limit}` (0 or 1) | |
| `disk_{read,write}_bytes_per_second`, `disk_busy_percent`, `disk_written_bytes_total` | `device` |
| `filesystem_{size,used,available}_bytes` | `path` |
| `network_{receive,transmit}_bytes_per_second`, `network_{receive,transmit}_errors_total` | `interface` |

A collector that is unavailable or fails in a round contributes nothing, so a
gap in a series means "not measured", never zero.

## Size

Each sample costs about 9 bytes (a one-byte series id and a float64), and
each round adds a small record header. Measured on a Pi 4: the history grows
by about **1.2 MB per hour, 29 MB per day**. Retention and downsampling (#17)
bound this.

## SD card writes

Flash wears out with writes, and an SD card has no wear levelling worth the
name. piway therefore keeps recorded samples in memory and writes them to
the segment file at most once per `flush_interval` (1 minute by default).

### Measured bytes written per hour

Pi 4, ext4 on the SD card (`noatime`, 5 s journal commits), default 1 s
interval. Each run measured 8 minutes of `/proc/diskstats` (sectors written
to `mmcblk0`) and of ext4 journal commits (`/proc/fs/jbd2/*/info`), after a
30 s warm-up; two rounds, each with its own baseline. Figures are per hour.

| `flush_interval` | Round 1 | Round 2 | Journal commits | Above baseline |
| ---------------- | ------- | ------- | --------------- | -------------- |
| recording off (baseline) | 4.2 MB | 2.2 MB | ~70 | – |
| `0` (every sample) | 17.3 MB | 20.2 MB | ~625 | **~16 MB** |
| `1m` (default) | 6.9 MB | 5.4 MB | ~150 | **~3 MB** |

The history itself is only ~1.2 MB per hour. Writing every second costs
about 13 times that on the card, because each write grows the file, which
dirties the inode, which costs an ext4 journal commit every 5 s. Writing
once a minute cuts the extra writes by about 5×, to roughly 2.5× the data.

Raw results (KiB per 8 minutes):

```
round  flush   disk_KiB    commits  dirtied_KiB  segment_KiB
1      off          580         12            0            0
1      0s          2368         82          224          176
1      1m           944         22          192          168
2      off          304          6            0            0
2      0s          2760         84          220          180
2      1m           736         18          192          172
```

`dirtied_KiB` (pages the process dirtied, `/proc/<pid>/io`) is nearly the
same for both settings: the difference is not the data but the metadata
and journal traffic around it. Background writes from other processes make
the baseline vary between rounds, so compare within a round.

## Durability

| Event | What is lost |
| ----- | ------------ |
| `systemctl stop`, Ctrl-C, SIGTERM | Nothing: pending samples are written and the segment is sealed. |
| piway crashes or is killed (`kill -9`) | Samples still in memory: up to `flush_interval`. |
| Power cut | Also what the kernel had not written yet (up to ~30 s more), but never earlier segments: they are sealed and fsynced. |

The live dashboard does not depend on the history, so none of these affect
it beyond the gap in the charts' past.
