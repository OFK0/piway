# piway

Self-hosted metrics agent and mini PaaS for Raspberry Pi, written in Go.

`git push` → build on the Pi → run in a container → served on a subdomain, with a live dashboard and an MCP server so Claude Code can operate it.

## Quick start

```sh
make build
./bin/piway -addr :8080
curl localhost:8080/healthz
curl localhost:8080/api/v1/snapshot   # see docs/API.md
go run ./cmd/wsload -clients 100      # WebSocket load test
```

## Run as a user service

```sh
install -m755 bin/piway ~/.local/bin/piway
mkdir -p ~/.config/systemd/user
cp deploy/systemd/piway.service ~/.config/systemd/user/
systemctl --user daemon-reload && systemctl --user enable --now piway
```

To change settings, copy `deploy/config.example.yaml` to
`~/.config/piway/config.yaml`, edit it, and restart the service. See
[docs/CONFIG.md](docs/CONFIG.md) for the full list, the `PIWAY_*` environment
variables, and the precedence rules.

## Roadmap

See [docs/ROADMAP.md](docs/ROADMAP.md) and the GitHub milestones.
