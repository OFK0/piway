# Deploying with git push

A push to a configured branch on GitHub tells piway to deploy that commit:
piway fetches it, builds its image on the Pi and replaces the app's
container. Deploys need rootless Docker ([DOCKER.md](DOCKER.md)); without a
Docker daemon piway logs the requests and the build API answers `503`.

## 1. Choose what deploys

List the repositories and branches in `apps_file`
(`~/.config/piway/apps.yaml` by default):

```yaml
deploy:
  - repo: OFK0/guestbook   # owner/name, as on GitHub
    branch: main
  - repo: OFK0/blog
    branch: release
```

Pushes to any other repository or branch are acknowledged and ignored, as
are tags and branch deletions. Unknown keys and duplicates are errors at
startup. Restart piway after editing the file.

## 2. Set a secret

```sh
openssl rand -hex 32
```

Give it to piway as `webhook_secret` (at least 16 characters). The
environment keeps it out of files: in `~/.config/systemd/user/piway.service`
add a line `Environment=PIWAY_WEBHOOK_SECRET=…` under `[Service]`, or set
it in `config.yaml`, readable only by you (`chmod 600`). `-print-config`
never shows it.

Without a secret, the webhook endpoint answers `503` and nothing deploys.

## 3. Add the webhook on GitHub

In the repository: **Settings › Webhooks › Add webhook**.

| Field | Value |
| ----- | ----- |
| Payload URL | `https://<your piway address>/api/v1/webhooks/github` |
| Content type | `application/json` (the default form encoding works too) |
| Secret | the secret from step 2 |
| Events | Just the push event |

GitHub sends a `ping` first; the delivery log on GitHub should show
`{"status":"pong"}`.

**GitHub must be able to reach piway.** A Pi on a home network is not
reachable from the internet; exposing it safely is #27 (Tailscale Funnel
or a Cloudflare Tunnel).

## What piway checks

| Check | Result |
| ----- | ------ |
| `X-Hub-Signature-256` is the HMAC-SHA256 of the body with the secret | Otherwise `401`, logged as a warning. Compared in constant time. |
| The delivery id (`X-GitHub-Delivery`) was not handled before | Otherwise `200 duplicate`: a redelivery or replay does not deploy twice. The last 1000 ids are remembered. |
| The event is `push` to `refs/heads/<branch>` of a listed repository | Otherwise `200 ignored` with the reason. |
| The body is at most 5 MiB | Otherwise `413`. |

A queued deploy answers `202 {"status":"queued"}`.

## What a deploy does

1. **Fetch** exactly the pushed commit, shallowly (`git fetch --depth 1`).
   Git never prompts: a private repository fails instead of hanging.
2. **Read `piway.yaml`** ([MANIFEST.md](MANIFEST.md)). An invalid manifest
   fails the build with its problems in the log. Static sites do not
   deploy yet (#76).
3. **Build** the image `piway/<name>:<commit>` with BuildKit for
   `linux/arm64`, natively on the Pi.
4. **Release** without downtime (blue/green on one host):
   1. Start the new container next to the one serving now, with `run.env`,
      the secrets, `run.command` and the limits, on a port Docker picks.
   2. Check its health there (`run.health`, below).
   3. If it never becomes healthy, remove it: **the previous version keeps
      serving** and the build fails with the reason.
   4. If it is healthy, switch the app's route to it in one step, let the
      old container finish requests in flight for 2 seconds, then stop it
      (SIGTERM, SIGKILL after 10 s) and remove it.
   5. Keep the app's 3 newest images and delete older ones.

### Routes

Each app has a stable address, `127.0.0.1:<port>`, with the port derived
from its name (20000–29999). piway listens there itself and proxies to
whichever container is current; switching versions only changes where the
proxy points, so no request is refused. With `proxy_domain` set, Caddy
serves `<name>.<domain>` from this address ([PROXY.md](PROXY.md)).

The trade-off: apps are reachable only while piway runs. When piway
starts, it finds the running app containers and serves them again.

### Health checks

`run.health` ([MANIFEST.md](MANIFEST.md)) decides when a new version may
take traffic: `GET <path>` on the new container must answer `2xx` or `3xx`.
Checks run every `interval` with a `timeout` each; failures during
`start_period` do not count, and after it 3 failures in a row reject the
version. A container that exits fails at once.

### Rollback

```sh
curl -s -X POST localhost:8080/api/v1/apps/<name>/rollback
```

re-releases the image the app ran before its current one, with the
`piway.yaml` it was built with: no fetch, no rebuild, the same health check
and switch as a deploy. It is queued and logged like a build (`"kind":
"rollback"`). Rolling back twice returns to the newer version. Only the
last 3 images per app are kept.

### Queue

One build runs at a time on the whole Pi. While it runs, new pushes queue;
for each repository branch only the newest push is kept, and the one it
replaces is marked `cancelled` ("superseded by …"). A build that takes more
than 30 minutes fails. Builds interrupted by a piway restart are marked
`failed`.

### Secrets

Values for `run.secrets` live on the Pi, in
`~/.config/piway/secrets/<name>.env` (next to `apps.yaml`):

```sh
mkdir -p ~/.config/piway/secrets && chmod 700 ~/.config/piway/secrets
echo 'DATABASE_URL=postgres://…' >> ~/.config/piway/secrets/guestbook.env
chmod 600 ~/.config/piway/secrets/guestbook.env
```

One `NAME=value` per line; `#` starts a comment. A build fails if a
declared secret has no value, and names which ones are missing.

### Limits

`run.memory` and `run.cpus` become cgroup limits. Raspberry Pi OS boots with
the memory controller off, and Docker then accepts a memory limit without
applying it; the build log says so with a `WARNING` line. See
[DOCKER.md](DOCKER.md#memory-limits-optional-needs-a-reboot).

## Following a build

Builds and their logs are kept under `data_dir/deploys` (the last 100):

```sh
curl -s localhost:8080/api/v1/builds                 # newest first
curl -s localhost:8080/api/v1/builds/<id>/log         # output so far
curl -s -X POST localhost:8080/api/v1/builds/<id>/cancel
```

`/api/v1/builds/<id>/log/stream` streams the log over a WebSocket while the
build runs. The dashboard's apps page (#26) builds on these.
