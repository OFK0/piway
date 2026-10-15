# App manifest (`piway.yaml`)

Every app deployed to piway has a `piway.yaml` at the root of its
repository. It says how to build the app and how to run it. piway reads it on
every deploy (#22, #23), so a change to the manifest ships with the commit
that makes it.

An app is one of two kinds:

- **Container app** (`build` + `run`): piway builds an arm64 image from a
  Dockerfile, runs it, and proxies `<name>.<domain>` to the port it listens on.
- **Static site** (`static`): piway optionally runs a build command in a
  throwaway container, then serves the resulting directory itself. No
  container keeps running ([DEPLOY.md](DEPLOY.md#static-sites)).

## Examples

### Node app

```yaml
piway: 1
name: guestbook

build:
  dockerfile: Dockerfile

run:
  port: 3000
  env:
    NODE_ENV: production
  secrets:
    - DATABASE_URL
  memory: 256MiB
  health:
    path: /healthz
```

The Dockerfile must produce an image for `linux/arm64`; a multi-arch base
image such as `node:22-alpine` does this without changes.

### Static site

```yaml
piway: 1
name: blog

static:
  dir: dist
  spa: false
  build:
    image: node:22-alpine
    command: npm ci && npm run build
```

### Smallest possible

```yaml
piway: 1
name: hello
static:
  dir: public
```

## Fields

| Field | Type | Default | Rules |
| ----- | ---- | ------- | ----- |
| `piway` | integer | required | Manifest version. Must be `1`. |
| `name` | string | required | 1–40 characters of `a-z`, `0-9` and `-`; starts with a letter, does not end with `-`. Becomes the subdomain. `piway` and `www` are reserved. |
| `build` | mapping | | Container app. Requires `run`; not allowed with `static`. |
| `build.context` | path | `.` | Directory sent to the image build. |
| `build.dockerfile` | path | `Dockerfile` | Relative to the repository root. |
| `build.args` | mapping of strings | | Build arguments (`ARG`). Not for secrets: they stay in the image history. |
| `run` | mapping | | Required with `build`. |
| `run.port` | integer | required | Port the app listens on inside the container, 1–65535. |
| `run.command` | list of strings | the image's `CMD` | Overrides the command, in exec form: `["node", "server.js"]`. |
| `run.env` | mapping of strings | | Plain environment variables. Names are `[A-Za-z_][A-Za-z0-9_]*`. |
| `run.secrets` | list of names | | Environment variables whose values are set on the Pi, never in the repository. A name cannot also be in `env`. |
| `run.memory` | size | `256MiB` | Memory limit, with a `MiB` or `GiB` suffix. At least `32MiB`. |
| `run.cpus` | number | `1` | CPU limit in cores, from `0.1` to `64`. |
| `run.health` | mapping | | HTTP health check used before switching traffic to a new version (#25). |
| `run.health.path` | string | `/` | Must start with `/`. Any `2xx` or `3xx` answer is healthy. |
| `run.health.interval` | duration | `10s` | Time between checks. |
| `run.health.timeout` | duration | `2s` | Must be shorter than `interval`. |
| `run.health.start_period` | duration | `30s` | Failures in this window after start do not count. |
| `static` | mapping | | Static site. Not allowed with `build` or `run`. |
| `static.dir` | path | required | Directory to serve, relative to the repository root. |
| `static.spa` | boolean | `false` | Serve `index.html` for paths that do not exist, for client-side routing. |
| `static.build` | mapping | | Runs before serving to produce `dir`. |
| `static.build.image` | string | required | Image the command runs in. |
| `static.build.command` | string | required | Shell command, run with `sh -c` in the repository root. |

- **Paths** are relative to the repository root, use `/`, and cannot be
  absolute or contain `..`.
- **Durations** use Go syntax: `500ms`, `10s`, `1m30s`.
- **Unknown fields are errors**, so a typo is never silently ignored.

## Errors

piway reports every problem it finds at once, each with its line number:

```text
piway.yaml:2: name: must be 1-40 lowercase letters, digits and "-", starting with a letter and not ending with "-", got "Guest_Book"
piway.yaml:6: run.port: must be between 1 and 65535, got 70000
piway.yaml:9: run.memmory: unknown field (did you mean "memory"?)
```
