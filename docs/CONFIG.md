# Configuration

piway reads its settings from four layers. Each layer overrides the ones
before it:

1. **Built-in defaults**
2. **Config file** (YAML)
3. **Environment variables** (`PIWAY_*`)
4. **Command-line flags** (only those given explicitly)

Run `piway -print-config` to see the effective configuration and which
layer each value came from:

```console
$ PIWAY_INTERVAL=2s piway -addr :9000 -print-config
# file: /home/user/.config/piway/config.yaml
addr: ":9000"  # flag -addr
interval: 2s  # env PIWAY_INTERVAL
stream_queue: 8  # default
log_level: warn  # file
```

The output is itself a valid config file.

## Settings

| Key | Env | Flag | Default | Description |
| --- | --- | ---- | ------- | ----------- |
| `addr` | `PIWAY_ADDR` | `-addr` | `":8080"` | HTTP listen address. |
| `interval` | `PIWAY_INTERVAL` | `-interval` | `1s` | Metric collection interval, as a Go duration (`500ms`, `2s`, `1m`). Minimum `100ms`. |
| `stream_queue` | `PIWAY_STREAM_QUEUE` | `-stream-queue` | `8` | Snapshots a WebSocket client may fall behind before it is disconnected with close code 1013. |
| `log_level` | `PIWAY_LOG_LEVEL` | `-log-level` | `info` | `debug`, `info`, `warn` or `error`. |

The pattern is the same for every setting: the env var is `PIWAY_` plus the
key in upper case, and the flag is the key with `_` replaced by `-`.

## Config file

The file is looked up in this order:

1. `-config <file>`
2. `$PIWAY_CONFIG`
3. `$XDG_CONFIG_HOME/piway/config.yaml`, usually `~/.config/piway/config.yaml`

A file named by `-config` or `PIWAY_CONFIG` must exist. The default location
is optional: if it is missing, piway runs on defaults.

The file is a flat YAML mapping. See
[`deploy/config.example.yaml`](../deploy/config.example.yaml):

```yaml
addr: "127.0.0.1:8080"
interval: 2s
log_level: debug
```

Quote addresses: in YAML, `[::1]:8080` without quotes is a list.

## Rules

- **Typos are errors.** An unknown key, a key set twice, or a nested value
  stops piway at startup with the file and line, instead of being silently
  ignored.
- **Same parser everywhere.** A value means the same thing in the file, in
  the environment and on the command line, and is validated the same way.
  Errors name the layer it came from:
  `interval (from env PIWAY_INTERVAL): time: invalid duration "soon"`.
- **Empty environment variables are ignored**, so a blank `Environment=`
  line in a systemd unit does not override the file.
- **A flag always wins**, even when its value equals the default. Avoid
  flags in the systemd unit, or the config file cannot change that setting.
- Invalid configuration exits with status 2.
