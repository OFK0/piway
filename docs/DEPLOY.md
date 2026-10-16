# Deploying with git push

A push to a configured branch on GitHub tells piway to deploy that commit:
piway fetches it, builds its image on the Pi and replaces the app's
container. Deploys need rootless Docker ([DOCKER.md](DOCKER.md)); without a
Docker daemon piway logs the requests and the build API answers `503`.

## 1. Choose what deploys

Add the repository from the dashboard (Apps, **Add repository**) or the
command line:

```sh
piway repo add OFK0/guestbook            # branch main
piway repo add OFK0/blog -branch release
piway repo list
piway repo remove OFK0/blog              # every branch; -branch to pick one
```

A running piway picks the change up within a moment; there is nothing to
restart. Removing a repository stops deploys from it. The app it built keeps
running with its data.

Both write `apps_file` (`~/.config/piway/apps.yaml` by default), which stays
the source of truth, so you can also edit it by hand:

```yaml
deploy:
  - repo: OFK0/guestbook   # owner/name, as on GitHub
    branch: main
  - repo: OFK0/blog
    branch: release
```

Pushes to any other repository or branch are acknowledged and ignored, as
are tags and branch deletions. Unknown keys and duplicates are errors. A hand
edit that does not parse is ignored, with a warning in piway's log, until it is
fixed; piway keeps deploying from the last good list.

### Several apps in one repository

An app can live in a directory of a repository, with its own piway.yaml
there: give its `path`.

```sh
piway repo add OFK0/shop -path apps/web
piway repo add OFK0/shop -path apps/api
```

```yaml
deploy:
  - repo: OFK0/shop
    branch: main
    path: apps/web
  - repo: OFK0/shop
    branch: main
    path: apps/api
```

The directory then stands in for the repository root: piway.yaml is read
there, and its paths (`build.context`, `build.dockerfile`, `static.dir`)
are relative to it; a static site's build command runs there too. A push
to the branch deploys every app of it, each as its own build; they do not
replace each other in the queue. In the dashboard, **Add repository** has
an **App directory** field. Each app's `name` in its piway.yaml must
differ.

### Private repositories

A private repository needs a credential, set per repository with `auth`. It
is the same for every branch of a repository. Public repositories need
nothing.

**Deploy key (recommended).** piway makes an SSH key pair for the
repository, and you add its public half to the repository as a read-only
deploy key. It opens that one repository, cannot write and does not expire;
no account-wide credential is involved.

```sh
piway repo add OFK0/private-app -private
```

This lists the repository with `auth: deploy-key` and prints the public key
with a link to the repository's **Settings › Deploy keys › Add deploy key**.
Paste it there and leave **Allow write access** off. Then check:

```sh
piway repo test OFK0/private-app
piway repo key OFK0/private-app   # print the public key again
```

The private key stays in `keys/` next to `apps.yaml`
(`~/.config/piway/keys/`), mode 0600. It never leaves the Pi: the API and the
dashboard show only the public key. Removing the repository's last branch
deletes its key; remove the deploy key on GitHub as well.

piway trusts only GitHub's published SSH host keys, which are built in, so a
first connection does not accept whatever key it is shown. If port 22 is
blocked on your network, piway connects to `ssh.github.com:443` instead
and keeps using it until it restarts.

**Fine-grained token.** With many private repositories, one token can be
simpler than a key for each. On GitHub, **Settings › Developer settings ›
Fine-grained tokens**: choose the repositories, give **Contents: Read-only**
and nothing else, and set an expiry you will remember. Then:

```sh
piway repo token set                      # paste it; it is not echoed
piway repo add OFK0/another-app -token
piway repo test OFK0/another-app
```

The token is stored in `keys/github-token`, mode 0600, and is used for every
repository with `auth: token`. git reads it through `GIT_ASKPASS`, so it is
never part of a URL or a command line and does not appear in build logs or
errors. When it expires, `piway repo token set` again; `piway repo token
remove` deletes it.

By hand, `apps.yaml` looks like this:

```yaml
deploy:
  - repo: OFK0/private-app
    branch: main
    auth: deploy-key        # or: token
```

For an entry written by hand, make its key with `piway repo add
OFK0/private-app -private`, which leaves the entry as it is.

In the dashboard, **Add repository** has an **Access** choice: Public,
Deploy key or Token. After adding, it shows the deploy key with a link to
GitHub's page for it, or takes the token, and has a **Test access** button.
The key icon on a private repository's row shows the same again.

`piway repo test`, **Test access** and `piway doctor` say what is wrong when piway cannot
read a repository:

| Message | What to do |
| ------- | ---------- |
| GitHub does not know this deploy key | Add the key from `piway repo key` to the repository. |
| does not exist, or this deploy key was added to a different repository | Check the name; a deploy key belongs to exactly one repository. |
| does not exist, or the token has no access to it | Add the repository to the token's list, with Contents: read. |
| GitHub rejected the token | It is wrong, expired or revoked: `piway repo token set`. |
| cannot reach GitHub | Check the Pi's network and DNS. |

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
| Payload URL | the address piway shows when you add the repository (see below) |
| Content type | `application/json` (the default form encoding works too) |
| Secret | the secret from step 2 |
| Events | Just the push event |

GitHub sends a `ping` first; the delivery log on GitHub should show
`{"status":"pong"}`.

**GitHub must be able to reach piway.** A Pi on a home network is not
reachable from the internet, and the address you open the dashboard at is
often tailnet-only (`https://<pi>.<tailnet>.ts.net`), which GitHub cannot
reach: a webhook pointed there fails with `We couldn't deliver this
payload: EOF`. Publish only the webhook path with Tailscale Funnel, on its
own port:

```sh
tailscale funnel --bg --https=10000 --set-path=/api/v1/webhooks/github http://127.0.0.1:8080/api/v1/webhooks/github
```

piway finds the public address by itself from `tailscale funnel status`:
the Funnel-enabled host that forwards `/api/v1/webhooks/github` to piway's
port, here `https://<pi>.<tailnet>.ts.net:10000/api/v1/webhooks/github`. The
dashboard's **Add repository** dialog and `piway repo add` show it. If piway
is published another way (a Cloudflare Tunnel, a reverse proxy), set it in
the config:

```yaml
webhook_url: https://hooks.example.com/api/v1/webhooks/github
```

When piway knows no public address, the dialog and the command say so
instead of offering the dashboard's address.

`piway doctor` checks it: with the `gh` CLI signed in, it reads each listed
repository's webhooks and fails for one whose Payload URL is not that
address, which says what to change it to.

## What piway checks

| Check | Result |
| ----- | ------ |
| `X-Hub-Signature-256` is the HMAC-SHA256 of the body with the secret | Otherwise `401`, logged as a warning. Compared in constant time. |
| The delivery id (`X-GitHub-Delivery`) was not handled before | Otherwise `200 duplicate`: a redelivery or replay does not deploy twice. The last 1000 ids are remembered. |
| The event is `push` to `refs/heads/<branch>` of a listed repository | Otherwise `200 ignored` with the reason. |
| The body is at most 5 MiB | Otherwise `413`. |

A queued deploy answers `202 {"status":"queued"}`. If the deploy queue
rejects it, piway answers `503` and releases the delivery ID so a redelivery
can try again. Deliveries already queued are still treated as duplicates.

## What a deploy does

1. **Fetch** exactly the pushed commit, shallowly (`git fetch --depth 1`).
   Git never prompts: a private repository fails instead of hanging.
2. **Read `piway.yaml`** ([MANIFEST.md](MANIFEST.md)). An invalid manifest
   fails the build with its problems in the log. Static sites go on
   [differently](#static-sites) from here.
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
whichever version is current, a container or a static site; switching
versions only changes where the route points, so no request is refused. With `proxy_domain` set, Caddy
serves `<name>.<domain>` from this address ([PROXY.md](PROXY.md)).

The trade-off: apps are reachable only while piway runs. When piway
starts, it finds the running app containers and serves them again, and
serves each static site's live version again.

### Static sites

An app whose `piway.yaml` has `static:` is served by piway itself from a
directory; no container keeps running for it.

1. **Build** (only with `static.build`): run `static.build.command` with
   `sh -c` in `static.build.image`, with the checkout mounted as the
   working directory. The container is removed when the command ends.
   It can reach the network (for `npm ci`), and with rootless Docker it
   runs as your user, not root.
2. **Copy** `static.dir` to its own directory,
   `~/.local/state/piway/deploys/sites/<name>/<build id>/`. Only
   directories and regular files are copied: symbolic links are skipped
   with a warning in the log, and `static.dir` itself must resolve inside
   the repository, so a link cannot make piway serve other files on the
   Pi. `.git` is left out.
3. **Release**: switch the app's route to the new directory in one step.
   If a container served the app before (it used to be a container app),
   it is stopped after 2 seconds and removed. The 3 newest sites per app
   are kept.

Files are served with `Content-Type` from their extension, `Last-Modified`,
range requests and `index.html` for directories. With `static.spa: true`,
a `GET` for a path that does not exist gets `/index.html`, for apps that
route in the browser. Otherwise it is a `404`.

### Health checks

`run.health` ([MANIFEST.md](MANIFEST.md)) decides when a new version may
take traffic: `GET <path>` on the new container must answer `2xx` or `3xx`.
Checks run every `interval` with a `timeout` each; failures during
`start_period` do not count, and after it 3 failures in a row reject the
version. A container that exits fails at once.

### Deploy without a push

GitHub cannot reach a Pi that is not on the internet, so pushes may never
arrive. Deploy by hand instead, with an [API token](API.md#api-tokens) or
from Claude Code ([MCP.md](MCP.md)):

```sh
# The newest commit of an app's branch (piway asks the remote: git ls-remote)
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/apps/<name>/deploy
# A repository branch in apps.yaml that was never deployed
curl -s -X POST -H "Authorization: Bearer $TOKEN" -d '{"repo":"owner/name","branch":"main"}' localhost:8080/api/v1/deploys
```

Either takes `"commit"` (a full SHA) to deploy that commit instead. The
build is the same as for a push, recorded with pusher `api`.

### Restart

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/apps/<name>/restart
```

starts a fresh container of the version that runs now, health-checks it
and switches to it like a deploy: no rebuild and no downtime. For an app
that hangs or leaks memory. Logged as a build with `"kind": "restart"`.

### Crashes

When an app's container exits on its own, a crash or an OOM kill past
`run.memory`, Docker starts it again. It comes back on a new port, and
piway follows it within a second (it watches Docker's events, and checks
every route every 30 seconds in case it missed one). An **App crashed**
alert says what happened ([ALERTS.md](ALERTS.md)); it resolves after 10
minutes without another crash. Stopping a container yourself
(`docker stop`, `docker kill`) is not a crash, and Docker leaves it
stopped: deploy or restart the app to bring it back.

### Rollback

```sh
curl -s -X POST localhost:8080/api/v1/apps/<name>/rollback
```

re-releases the image the app ran before its current one, with the
`piway.yaml` it was built with: no fetch, no rebuild, the same health check
and switch as a deploy. For a static site it serves the previous site's
directory again. It is queued and logged like a build (`"kind":
"rollback"`). Rolling back twice returns to the newer version. Only the
last 3 images and sites per app are kept.

Secrets are not versioned: a rollback (and a restart) runs the old build
with the secret values stored now, not the ones it was first deployed with.

### Queue

One build runs at a time on the whole Pi. While it runs, new pushes queue;
for each repository branch only the newest push is kept, and the one it
replaces is marked `cancelled` ("superseded by …"). A build that takes more
than 30 minutes fails. Builds interrupted by a piway restart are marked
`failed`.

### Secrets

Values for `run.secrets` live on the Pi, never in the repository. Set them
on the app's page, under **Environment**: each secret shows whether it is
set, its value is masked until you choose to show it, and **Add secret**,
the pencil and the bin set, change and remove values. Values must be one
line.

They are stored in `~/.config/piway/secrets/<name>.env` (next to
`apps.yaml`), which you can also edit by hand:

```sh
mkdir -p ~/.config/piway/secrets && chmod 700 ~/.config/piway/secrets
echo 'DATABASE_URL=postgres://…' >> ~/.config/piway/secrets/guestbook.env
chmod 600 ~/.config/piway/secrets/guestbook.env
```

One `NAME=value` per line; `#` starts a comment. The dashboard keeps
comments and other lines when it changes the file, and writes it
atomically with mode 0600. A build fails if a declared secret has no value,
and names which ones are missing. A name in the file that piway.yaml does
not declare is shown as *not declared*; the app does not get it.

A new value applies when the app next starts: deploy or **Restart**. The
container is labelled with a digest of the values it started with, so the
page says **Restart needed** when they changed since, and the restart
switches to a fresh container without downtime.

Secret values are only for a signed-in browser. With an API token, or
through the MCP server, the environment shows names and whether each is
set, never values, and secrets cannot be changed. Responses with values are
not cached, and values are never logged.

### Volumes

Every deploy starts a new container, so anything an app writes inside it
is gone after the next one. Data that must last goes in a volume:

```yaml
run:
  port: 3000
  volumes:
    - data:/data        # name:/path inside the container
```

piway creates the Docker volume `piway-<app>_<name>` on the first release
that declares it and mounts it at that path in every container of the app
after that. It survives deploys, restarts and rollbacks. During a deploy the
old container keeps serving while the new one starts, so **both mount the
same volume for a few seconds**; see SQLite below.

The app's page lists its volumes with their size. A volume piway.yaml no
longer declares is kept, data and all, and marked *not declared*; it can be
deleted there once no container uses it. piway never deletes a volume on
its own: removing the repository or the app leaves its volumes in place.

With rootless Docker the files belong to the user piway runs as, under
`~/.local/share/docker/volumes/`. `piway backup` includes every volume
([BACKUP.md](BACKUP.md)).

#### State in files

During a deploy the old and the new version run side by side for a few
seconds, and the old one is stopped (SIGTERM) only after traffic switched.
State an app keeps in files on a volume must survive that overlap: a file
the new version reads when it starts may still change, because the old one
keeps working until it stops. Either share the state while both run
(SQLite, below, or a database from `run.databases`), or hand it over
explicitly: the old version writes a final copy on SIGTERM, and the new one
waits for it before writing its own. The
[events example](https://github.com/OFK0/piway/tree/main/examples/events)
does the second.

#### SQLite

SQLite works well on a volume, with two rules:

- **Use WAL mode** (`PRAGMA journal_mode=WAL;` once, when the database is
  opened). Readers and a writer can then work at the same time, which is
  what happens while two containers share the file during a deploy. Keep the
  `-wal` and `-shm` files next to the database, in the volume. Set a busy
  timeout (`PRAGMA busy_timeout=5000;`) so a write that meets the other
  container's lock waits instead of failing.
- **Make migrations backward compatible.** The new version may migrate the
  database while the old one still serves requests against it. Add columns
  and tables first; drop or rename them in a later deploy, once no running
  version needs them. A rollback runs the previous image against the
  current schema, so the same rule keeps rollbacks safe.

Do not copy a live database file to back it up. `piway backup` copies
SQLite databases with SQLite's online backup, which is consistent while
the app runs ([BACKUP.md](BACKUP.md#sqlite-databases)).

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
