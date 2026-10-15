# Contributing to piway

How work is planned, written, committed and merged in this repository.

## Language

Everything in the repository is written in English: code, comments, commit
messages, branch names, issues, pull requests and docs.

## Issues

Work is issue-driven. Every change starts from a GitHub issue.

- Use the **Task** template: `Goal`, `Learning focus` (optional), `Acceptance criteria` as a checklist.
- Labels:
  - one `type:` label: `type:feature`, `type:chore` or `type:docs`. Use `bug` for defects.
  - one or more `area:` labels: `agent`, `dashboard`, `storage`, `deploy`, `security`, `mcp`, `infra`.
  - add `learning` when the issue exists mainly to practise a concept.
- Assign the milestone (`M0`–`M6`) the work belongs to. See [docs/ROADMAP.md](docs/ROADMAP.md).
- When a decision changes an existing issue (a library choice, a scope split), comment on that issue instead of silently changing it.

## Branches

```
<type>/<issue>-<short-slug>
```

For example: `feat/9-websocket-hub`, `fix/52-thermal-log-flapping`, `docs/2-dev-setup`.
`<type>` is the Conventional Commits type of the main change. One branch per issue.

## Commits

Commits follow [Conventional Commits 1.0](https://www.conventionalcommits.org/en/v1.0.0/).

```
<type>(<scope>): <subject>

<body>

<footer>
```

### Type

| Type | Use for |
| ---- | ------- |
| `feat` | A user-visible feature: a new endpoint, setting, metric or screen. |
| `fix` | A bug fix. |
| `perf` | A change whose goal is speed or lower resource use. |
| `refactor` | A code change that neither fixes a bug nor adds a feature. |
| `test` | Adding or fixing tests only. |
| `docs` | Documentation only. |
| `build` | The build system or dependencies: `Makefile`, `go.mod`, `package.json`. |
| `ci` | GitHub Actions workflows. |
| `chore` | Anything else that does not touch shipped code (repo settings, templates). |
| `style` | Formatting only, no behaviour change. |

### Scope

Use the part of the codebase the change is about. Omit the scope only for
changes that span the whole repository.

| Scope | Path |
| ----- | ---- |
| `agent` | `internal/agent` |
| `collector` | `internal/collector/*`, `internal/procfs` |
| `server` | `internal/server` |
| `stream` | `internal/stream` |
| `config` | `internal/config` |
| `docker` | `internal/docker` (Engine API client) |
| `webhook` | `internal/webhook` (GitHub push webhooks) |
| `pipeline` | `internal/deploy` (build pipeline) |
| `router` | `internal/router` (stable app addresses) |
| `caddy` | `internal/caddy`, `docs/PROXY.md` (app domains) |
| `mdns` | `internal/mdns` (app names on the LAN) |
| `web` | `web/` (dashboard), `internal/webui` (its embedding) |
| `cli` | `cmd/*` |
| `deploy` | `deploy/` (systemd, example configs) |
| `api` | `docs/API.md` and API compatibility |
| `tsdb` | `internal/tsdb` |
| `prom` | `internal/prom` (the `/metrics` endpoint) |
| `recorder` | `internal/recorder` (snapshots to the store) |
| `history` | `internal/history` (rollups and retention) |
| `manifest` | `internal/manifest`, `docs/MANIFEST.md` (`piway.yaml`) |

Add new scopes here when a new package or area appears.

### Subject

- Imperative mood, lower case after the colon, no trailing period: `feat(stream): add WebSocket fan-out hub`.
- At most 72 characters including the type and scope.
- Say what changes for the user or the system, not which files were edited.

### Body

- Explain **why** and the decisions that are not obvious from the diff. Wrap at 72 columns.
- A bullet list is fine for several related points.
- Include measurements when the change is about performance or limits (for example, load test results).

### Footer

- `Closes #<issue>` for the issue the commit completes. `Refs #<issue>` for related ones.
- `BREAKING CHANGE: <what breaks and how to migrate>` for any incompatible change to the HTTP API, config keys, CLI flags or on-disk formats. Also add `!` after the type or scope: `feat(api)!: rename snapshot fields`.
- No AI attribution lines (`Co-Authored-By: Claude …`, `Generated with …`).

### Example

```
feat(config): load settings from file, PIWAY_* env and flags

Precedence is defaults < YAML file < environment < flags that were set
explicitly. A single field table drives every layer, so a value is
parsed and validated the same way wherever it comes from.

- Unknown or duplicate keys fail at startup with the file line.
- -print-config shows the effective config and each value's source.

Closes #10
```

## Pull requests

- **Title:** a Conventional Commits subject, because PRs are squash-merged and the title becomes the commit on `main`. For example: `feat(stream): add WebSocket live stream with fan-out hub`.
- **Body**, in this order:
  1. `Closes #<issue>`
  2. `## What`: the change as a short list.
  3. `## Design`: a `Decision | Why` table for the choices a reviewer would question.
  4. `## Testing`: what was tested and how (unit, integration, manual runs on the Pi, with numbers where relevant).
  5. Known limitations or follow-ups, if any.
- CI (`go` and `web` jobs) must pass before merging.
- Merge with **squash** and delete the branch. `main` stays linear, one commit per issue.
- A PR that depends on an unmerged PR says so at the top of the body and is rebased onto `main` once the dependency is merged.

## Code

### Go

- `make lint` (gofmt + go vet) and `make test` must pass.
- The race detector does not run on the Pi's 39-bit VMA kernel. `make test-race` runs in CI.
- Prefer table-driven tests. Use `httptest` for handlers. Bound every blocking wait in tests with a timeout so a bug fails the test instead of hanging it.
- Accept interfaces where the consumer needs them, and define them on the consumer side.

### Web

- `make web-lint`, `make web-test` and `make web` must pass.
- Logic that does not need a browser (buffers, backoff, stream state) lives
  in plain modules under `web/src/lib` with `*.test.ts` next to them, run by
  Node's built-in test runner. Keep them free of `@/` imports and
  TypeScript-only syntax such as parameter properties, so Node can strip
  the types and run them directly.
- Follow [web/DESIGN.md](web/DESIGN.md) once it exists: semantic tokens only, no raw colors in components.

### Licenses

piway is MIT-licensed. New dependencies must have a license compatible with
that (MIT, ISC, BSD, Apache-2.0, OFL for fonts). `make build` generates
`THIRD_PARTY_NOTICES.txt` from what is actually bundled and fails if a
bundled package has no license file. If a package really ships none, keep
its upstream license text in `web/licenses/` and add an `overrides` entry
in `web/vite.config.ts`, with a comment saying where the text came from.

### Docs stay in sync

| When you change | Update |
| --------------- | ------ |
| An HTTP endpoint or its JSON | `docs/API.md` and `web/src/api.ts` |
| A config setting | `docs/CONFIG.md` and `deploy/config.example.yaml` |
| The roadmap or a milestone's scope | `docs/ROADMAP.md` |

## Privacy

The repository is public. Never commit personal or machine-specific data:

- **Test fixtures:** synthetic only. No real hostnames, IP addresses, MAC addresses, network interface names other than generic ones (`eth0`, `wlan0`), usernames, or home directory paths.
- **Docs and examples:** use placeholders such as `<pi-address>`, `localhost`, or `/home/user`.
- **Secrets:** never in the repository. Configuration that needs a secret reads it from the environment or a file outside the repo.
