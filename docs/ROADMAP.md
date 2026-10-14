# piway roadmap

Each milestone maps to a GitHub milestone; each bullet is an issue.

## M0 — Foundation
- Project scaffold: Go module, Makefile, CI, systemd unit
- Developer setup docs (Go on Pi, cross-compiling from laptop)

## M1 — Metrics agent
- CPU usage collector (`/proc/stat`)
- Memory collector (`/proc/meminfo`)
- Temperature & throttling collector (`/sys/class/thermal`, firmware throttled flags)
- Disk & network collectors (`/proc/diskstats`, `/proc/net/dev`)
- Collector scheduler and snapshot model (goroutines, ticker, context cancel)
- `GET /api/v1/snapshot` JSON endpoint
- WebSocket live stream with a fan-out hub (slow-client handling, backpressure)
- Config file + env overrides

## M2 — Live dashboard
- React + Vite + TS app under `web/`
- Embed built dashboard into the Go binary (`embed.FS`)
- Live charts over WebSocket with reconnect/backoff
- Release pipeline: GitHub Actions builds arm64 binary on tag

## M3 — Storage
- Append-only segment store for time series
- Write batching to spare the SD card
- Retention + downsampling (raw → 1m → 1h)
- Range query API `GET /api/v1/series`
- Prometheus `/metrics` endpoint

## M4 — Deploy engine
- Docker on the Pi + Go client for the Docker Engine API
- App manifest (`piway.yaml`) spec
- GitHub webhook receiver with HMAC signature verification
- Build pipeline: clone → arm64 image build → run, with streamed logs
- Caddy integration via admin API (subdomain per app, TLS)
- Health checks, zero-downtime swap and rollback
- Apps page in the dashboard

## M5 — Exposure & security
- Remote access via Tailscale or Cloudflare Tunnel
- Dashboard/API authentication
- Host hardening guide (SSH keys, ufw, fail2ban, unattended upgrades)
- Alert rules + Web Push notifications (needs HTTPS from remote access and authentication)

## M6 — MCP server
- MCP server in Go (stdio + HTTP transport)
- Tools: `get_status`, `list_apps`, `deploy`, `restart_app`, `tail_logs`
- Claude Code setup docs + demo
