# Development

How to build piway from source, run its tests, and get a changed binary
onto a Pi quickly. To *use* piway, see [INSTALL.md](INSTALL.md) instead.

You can develop in two places, and both end with the same commands:

- **On the Pi itself**, over SSH. Nothing to cross-compile. On a Pi 4 a
  first `make build` takes about 2 minutes, later ones about 35 seconds
  (mostly the dashboard), and `make build-go` a few seconds.
- **On a laptop or desktop** (Linux, macOS or Windows with WSL), then
  cross-compile for the Pi and copy the binary over with one command.

## Tools

| Tool | Version | Used for |
| ---- | ------- | -------- |
| Go | the one in [`go.mod`](../go.mod) | the binary |
| Node.js | the one in [`web/.nvmrc`](../web/.nvmrc) (24) | the dashboard |
| git, make | any recent | building |
| Docker | rootless, as in [DOCKER.md](DOCKER.md) | deploys, only when you work on them |

### Go and Node on the Pi, without sudo

Both are single archives; unpack them into `~/.local` and put their `bin`
directories on your `PATH`:

```sh
cd ~/piway                                   # your checkout
GO=$(sed -n 's/^go //p' go.mod)
curl -fsSL "https://go.dev/dl/go$GO.linux-arm64.tar.gz" | tar -C ~/.local -xz

# Node: the newest 24.x for arm64 (see https://nodejs.org/dist/latest-v24.x/)
NODE=$(curl -fsSL https://nodejs.org/dist/latest-v24.x/SHASUMS256.txt | grep -o 'node-v[0-9.]*-linux-arm64.tar.xz' | head -1)
curl -fsSL "https://nodejs.org/dist/latest-v24.x/$NODE" | tar -C ~/.local -xJ
mv ~/.local/"${NODE%.tar.xz}" ~/.local/node

echo 'export PATH="$HOME/.local/go/bin:$HOME/.local/node/bin:$HOME/go/bin:$PATH"' >> ~/.bashrc
. ~/.bashrc
go version && node --version
```

On a laptop, install Go and Node however you usually do.

## Build and test

```sh
make build        # the dashboard (web/), then bin/piway with it embedded
make build-go     # bin/piway without the dashboard: API only, no Node needed
make test         # Go tests
make lint         # gofmt and go vet
make web-test     # dashboard unit tests
make web-lint     # dashboard lint (oxlint)
```

Run all four checks before committing ([CONTRIBUTING.md](../CONTRIBUTING.md)).
`make e2e` runs the end-to-end suite: the real binary against real Docker,
with the example apps deployed by signed webhooks ([e2e/README.md](../e2e/README.md)).
It takes 20 to 40 minutes, so CI runs it before every release rather than on
every push.

`make test-race` runs the tests with the race detector; it needs a 48-bit
address space, so it fails on Raspberry Pi OS kernels and runs in CI
instead.

### Run your build

On a Pi that already runs piway, keep your build apart from it: another
port, no config file, its own data, password and apps files. And give it
a time limit, so a forgotten server does not hold the port:

```sh
mkdir -p /tmp/piway-dev
timeout -s INT 10m ./bin/piway -config /dev/null -addr 127.0.0.1:18080 \
  -data-dir /tmp/piway-dev -password-file /tmp/piway-dev/password -apps-file /tmp/piway-dev/apps.yaml
curl localhost:18080/healthz
curl localhost:18080/api/v1/snapshot   # no password set, so the same machine gets in
```

Without these flags it would read the real `~/.config/piway` files and
share their password and apps.

### The dashboard with hot reload

```sh
# the API, started as above on 127.0.0.1:18080
cd web && PIWAY_API=http://localhost:18080 npm run dev   # http://localhost:5173, proxies /api to it
```

`npm run dev` alone proxies to `http://localhost:8080`;
`PIWAY_API=http://<pi>:8080` develops the dashboard on a laptop against a
piway on the Pi. See [web/README.md](../web/README.md)
for the design system and components.

### Other tools

```sh
go run ./cmd/wsload -clients 100 -url ws://localhost:18080/api/v1/stream   # WebSocket load test
cd site && npm ci && npm run dev       # the documentation site, from docs/
```

## Put a build on the Pi

Both commands replace only the binary in `~/.local/bin/piway` and restart
the service, then wait for `/healthz`. They expect a Pi that has been set
up once with `piway setup` ([INSTALL.md](INSTALL.md)); Docker, Caddy, the
config and the password are left alone.

**On the Pi**, from your checkout:

```sh
make install
```

**From a laptop**, with SSH access to the Pi by key:

```sh
make deploy-pi PI=pi@raspberrypi.local
```

It cross-compiles (`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`, dashboard
included) into `bin/piway-linux-arm64`, copies it with `scp` and swaps it
in over `ssh`. `make pi` does only the build. If piway listens somewhere
other than `127.0.0.1:8080` on the Pi, add `PI_ADDR=<host:port>`.

The new binary is first copied next to the old one and then renamed over
it, because copying over a running executable fails ("Text file busy").
To go back to a release, run the installer again ([INSTALL.md](INSTALL.md)).

## Where to go next

- [CONTRIBUTING.md](../CONTRIBUTING.md): issues, branches, commit messages
  and pull requests.
- [RELEASING.md](RELEASING.md): cutting a release and its reproducible
  archive.
- [API.md](API.md): what the dashboard talks to.
