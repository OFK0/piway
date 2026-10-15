<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/piway-logo-white.svg">
    <img src="docs/brand/piway-logo-black.svg" alt="piway" width="240">
  </picture>
</h1>

Self-hosted metrics agent and mini PaaS for Raspberry Pi, written in Go.

`git push` → build on the Pi → run in a container → served on a subdomain, with a live dashboard and an MCP server so Claude Code can operate it.

## Quick start

Requires Go (see `go.mod`) and Node.js 24 for the dashboard.

```sh
make build                            # builds web/, then a binary that embeds it
./bin/piway -addr :8080
curl localhost:8080/healthz
# dashboard: open http://<pi-address>:8080/ in a browser
curl localhost:8080/api/v1/snapshot   # see docs/API.md
go run ./cmd/wsload -clients 100      # WebSocket load test
```

`make build-go` builds the binary without the dashboard (API only, no Node
needed).

### Dashboard development

```sh
./bin/piway &                         # API on :8080
cd web && npm run dev                 # UI with hot reload on :5173, proxies the API
```

See [web/README.md](web/README.md).

## Install a release

Each release has a ready-to-run binary for the Pi (`linux/arm64`) with the
dashboard built in. See [docs/RELEASING.md](docs/RELEASING.md#installing-on-the-pi)
for downloading, verifying the checksum and installing it.

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

## Use it from Claude Code

piway is an [MCP](https://modelcontextprotocol.io) server, so Claude Code
can check on the Pi and deploy to it for you:

![Claude Code asks piway how the Pi is doing, then deploys a new commit and waits for it to go live](docs/demo/claude-code.gif)

*A replay of a real session against a test instance on a Pi 4: the prompts,
tool calls and answers are the recorded ones, shown in order.*

1. In the dashboard, open **Settings → API tokens** and create a token
   named `claude-code`. It is shown once.
2. Add piway to Claude Code, over HTTP (through
   [Tailscale](docs/REMOTE.md) from anywhere on your tailnet):

   ```sh
   claude mcp add -s user --transport http piway https://<pi>.<tailnet>.ts.net/mcp \
     --header "Authorization: Bearer piway_…"
   ```

   or over stdio, with Claude Code running on the Pi itself:

   ```sh
   claude mcp add -s user piway -e PIWAY_TOKEN=piway_… -- piway mcp
   ```

3. Check with `claude mcp list`, then ask things like *"Is anything wrong
   on the Pi?"*, *"Why did the last deploy of blog fail?"* or *"Deploy the
   newest commit of photos."*

Claude gets six tools: `get_status`, `list_apps` and `tail_logs` read;
`deploy`, `restart_app` and `rollback_app` change what runs and only act
with `confirm: true`, which Claude sets when you asked for it. The token
can do what any API token can and nothing more. Details:
[docs/MCP.md](docs/MCP.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the issue, branch, commit
(Conventional Commits) and pull request conventions.

## Roadmap

See [docs/ROADMAP.md](docs/ROADMAP.md) and the GitHub milestones.

## License

piway is released under the [MIT license](LICENSE). The binary also contains
third-party code and fonts under their own licenses; `make build` collects
them into `THIRD_PARTY_NOTICES.txt`, which ships with every release and is
served by the dashboard (footer › Licenses).
