# Deploying with git push

A push to a configured branch on GitHub tells piway to deploy that commit.
The build pipeline that turns the commit into a running app is #23; until
it lands, piway logs each deploy request.

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
