# Alerts

piway watches its own metrics and deploys, and says when something goes
wrong and when it is fine again: in its log, in the dashboard (Settings),
by [`GET /api/v1/alerts`](API.md#get-apiv1alerts), and as
[push notifications](#push-notifications) on your phone or computer.

## Built-in rules

Without an alerts file, piway uses these:

| Rule | Fires when | For | Resolves after |
| ---- | ---------- | --- | -------------- |
| CPU is hot | `temperature_celsius` > 75 | 2m | 1m back below |
| CPU is throttled | `throttle_throttled` > 0 | 1m | 1m |
| Power supply is too weak | `throttle_under_voltage` > 0 | 10s | 1m |
| Disk is almost full | `filesystem_used_percent` > 90, per filesystem | 5m | 1m |
| Memory is almost full | `memory_used_percent` > 90 | 5m | 1m |
| Deploy failed | a deploy of a repository branch fails | at once | its next successful deploy or rollback |

The Pi 4 starts throttling at 80 °C, so "CPU is hot" warns before that.

## Your own rules

Put rules in `~/.config/piway/alerts.yaml` (setting `alerts_file`,
[CONFIG.md](CONFIG.md)) and restart piway. The file replaces the built-in
rules: copy the ones you want to keep.

```yaml
rules:
  - name: CPU is hot
    metric: temperature_celsius
    above: 70
    for: 1m
  - name: Root disk almost full
    metric: filesystem_used_percent{path="/"}
    above: 85
    for: 10m
    clear_for: 30m
  - name: Network is down
    metric: network_receive_bytes_per_second{interface="wlan0"}
    below: 1
    for: 5m

# Alert when a deploy fails (default true).
deploy_failures: true
```

| Field | Required | Meaning |
| ----- | -------- | ------- |
| `name` | yes | Shown in notifications. Unique, at most 80 characters. |
| `metric` | yes | A series name from the list below. Without labels it watches every labelled series of that name separately (each filesystem, each interface); with labels, only that one. |
| `above` / `below` | one of them | The threshold. The rule is breached while the value is strictly above (below) it. |
| `for` | no, `0s` | How long the value must stay breached before the alert fires. |
| `clear_for` | no, `1m` | How long it must stay back before the alert resolves. |

Durations use Go syntax (`30s`, `5m`, `1h30m`), at most `24h`. Unknown
fields and unknown metrics are errors at startup, with the rule's index.

## How an alert moves

```text
ok ──breach──► pending ──breached for `for`──► firing ──back──► clearing ──back for `clear_for`──► resolved
      ▲             │                                ▲               │
      └────back─────┘                                └───breach──────┘
```

Only **firing** and **resolved** are announced. A value hovering around
the threshold therefore cannot cause a stream of notifications: it must
stay over it for `for` to fire, and under it for `clear_for` to resolve,
and a breach while clearing just returns to firing silently.

A pending alert whose series disappears (an unmounted disk) is dropped; a
firing one stays until its series is seen to be fine. Alert state lives
in memory: after a restart, conditions that still hold fire again.

## Push notifications

Turn them on in the dashboard: **Settings → Notifications → Notify this
device**, on every phone or computer that should get alerts. "Send a
test" checks the whole path.

- **HTTPS is required**: browsers only allow notifications on secure
  pages. Open piway through Tailscale ([REMOTE.md](REMOTE.md)), or on the
  Pi itself at `localhost`.
- **iPhone and iPad** (iOS 16.4 or later): only web apps on the Home
  Screen get notifications. In Safari tap Share → Add to Home Screen,
  open piway from the new icon, sign in, then turn notifications on.
- Notifications arrive with the dashboard closed: a small service worker
  (`/sw.js`) shows them and opens the dashboard when tapped.
- A resolved alert replaces its firing notification on the device, and
  says it is resolved.

### Deploys

With `notify_deploys` ([CONFIG.md](CONFIG.md)) at its default, `all`,
every finished deploy sends one notification:

| Event | Notification | Opens |
| ----- | ------------ | ----- |
| A deploy succeeds | **Deployed pinboard** · commit 740f82f · in 1 min 6 s · pinboard.local:8443 | the build's log |
| A rollback or restart succeeds | **Rolled back pinboard**, **Restarted pinboard** | the build's log |
| A deploy fails | **Deploy failed** · Deploy of pinboard failed: build: exit status 1 | Apps |

Each repository branch has one tag, so on a device the newest
notification about it replaces the one before: after a fix, "Deployed"
replaces "Deploy failed". Cancelled builds send nothing.

- `notify_deploys: failures` sends only failures, and "Resolved: Deploy
  failed" with the next success.
- `notify_deploys: off` sends none; failures still show as alerts in the
  dashboard and the API.

How it works, with no account anywhere: each browser gives piway a
subscription from its own push service (Google's for Chrome and Android,
Mozilla's, Apple's, Microsoft's). piway encrypts every message for that
browser alone (RFC 8291), signs the request with its own key (VAPID,
RFC 8292) and posts it to the push service, which only relays it and
cannot read it.

- piway's key pair is made on first start, in `<data_dir>/push/vapid.json`.
  Deleting it makes every device subscribe again.
- Subscriptions are kept in `<data_dir>/push/subscriptions.json`. One the
  push service reports gone (the browser unsubscribed or was reset) is
  removed at the next send.
- piway only sends to the push services above (an allowlist), and never
  follows redirects, so a subscription cannot make it contact anything
  else.
- `push_contact` ([CONFIG.md](CONFIG.md)) is the contact that push
  services see in the signed request; set your own `mailto:` if you like.

## Metrics

The same names as [`GET /api/v1/series`](API.md#get-apiv1series), from
every snapshot:

| Metric | Labels | Unit |
| ------ | ------ | ---- |
| `cpu_usage_percent` | | % of all cores |
| `cpu_core_usage_percent` | `core` | % |
| `memory_used_percent` | | % of RAM in use (not counting cache) |
| `memory_{total,used,available,cached,buffers}_bytes`, `swap_{total,used}_bytes` | | bytes |
| `temperature_celsius` | | °C |
| `throttle_under_voltage`, `throttle_freq_capped`, `throttle_throttled`, `throttle_soft_temp_limit` | | 1 while active, else 0 |
| `disk_{read,write}_bytes_per_second` | `device` | bytes/s |
| `disk_busy_percent` | `device` | % |
| `disk_written_bytes_total` | `device` | bytes since boot |
| `filesystem_used_percent` | `path` | % as `df` shows it |
| `filesystem_{size,used,available}_bytes` | `path` | bytes |
| `network_{receive,transmit}_bytes_per_second` | `interface` | bytes/s |
| `network_{receive,transmit}_errors_total` | `interface` | errors and drops since boot |
