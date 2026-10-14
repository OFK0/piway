# piway: notes for Claude Code

Self-hosted metrics agent and mini PaaS for a Raspberry Pi 4, written in Go,
with a React dashboard in `web/`. It is developed on the Pi itself.

**Follow [CONTRIBUTING.md](CONTRIBUTING.md)** for issues, branches,
Conventional Commits, PR format, squash merges and the privacy rules. The
points below are easy to miss.

## Workflow

- Start from an issue; branch as `<type>/<issue>-<slug>`.
- Commit messages and PR titles use Conventional Commits with a scope from
  the table in CONTRIBUTING.md. PR titles matter most: squash merge turns
  them into the commit on `main`.
- No AI attribution lines in commits or PRs.
- Merge only when the user asks. Use `gh pr merge <n> --squash --delete-branch`.
- When a decision affects another issue, comment on that issue.

## Running things on the Pi

- Go, gh and Node live under `~/.local`. There is no sudo.
- `go test -race` does not work here (39-bit VMA). CI runs it.
- Never start `bin/piway` without a bound: use `timeout -s INT <n>` or kill it
  afterwards. A forgotten server keeps running and holds the port.
- Use ports other than `:8080` for experiments, such as `127.0.0.1:18080`, in
  case the user's own service is running.

## Checks before committing

```sh
make lint && make test     # Go
make web-lint && make web  # dashboard
```
