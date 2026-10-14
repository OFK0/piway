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
   fails the build with its problems in the log. Static sites wait for the
   Caddy integration (#24).
3. **Build** the image `piway/<name>:<commit>` with BuildKit for
   `linux/arm64`, natively on the Pi.
4. **Release**: remove the app's previous container, start
   `piway-<name>` from the new image with `run.env`, the secrets,
   `run.command` and the limits, published on `127.0.0.1:<port>`, and
   check it is still running after 5 seconds. Until #25 there is a short
   gap between the old container stopping and the new one starting, and no
   automatic rollback.

The port is derived from the app name (20000–29999), so it stays the same
across deploys; Caddy (#24) will proxy `<name>.<domain>` to it.

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
