# MCP server

piway speaks the [Model Context Protocol](https://modelcontextprotocol.io),
so an AI assistant such as Claude Code can look at the Pi and operate it:
"is anything wrong on the Pi?" becomes a `get_status` call.

Every tool works through piway's [HTTP API](API.md) with the client's own
credentials, so MCP can do exactly what an [API token](API.md#api-tokens)
can, and nothing else.

## Connect Claude Code

First create an API token in the dashboard: **Settings → API tokens**,
named after the client (`claude-code`). Then pick a transport.

### Over HTTP (piway on the Pi, the client anywhere on your tailnet)

```sh
claude mcp add --transport http piway https://<pi>.<tailnet>.ts.net/mcp \
  --header "Authorization: Bearer piway_…"
```

piway serves MCP at `/mcp` on its usual address, behind the same sign-in
as the API ([REMOTE.md](REMOTE.md) for reaching it from elsewhere).

### Over stdio (the client starts `piway mcp`)

```sh
claude mcp add piway -e PIWAY_TOKEN=piway_… -- piway mcp
```

`piway mcp` speaks MCP on its stdin and stdout and forwards to a running
piway, by default `http://127.0.0.1:8080`. On another machine, point it at
the Pi with `-e PIWAY_URL=https://<pi>.<tailnet>.ts.net`, or use
`-token-file` instead of the environment. Logs go to stderr.

Add `-s user` to either command to have piway in every project, not
just the current one. Check with `claude mcp list`, then ask Claude about
the Pi.

## Tools

| Tool | Changes something | What it does |
| ---- | ----------------- | ------------ |
| `get_status` | no | CPU load and temperature, throttling, memory and disk use, active alerts, how many apps are deployed. |
| `list_apps` | no | Each app: repository, address, the commit it runs (container or static site), its latest build if that failed; and branches in `apps.yaml` never deployed. |
| `tail_logs` | no | `app`: the last `lines` (default 100, at most 1000) of its running container. `build`: the build's state and the end of its log; with `wait_seconds` (up to 300) it first waits for the build to finish. |
| `deploy` | **yes** | Build and release the newest commit (or `commit`) of an `app`, or of a `repo` and `branch` in `apps.yaml`. Same as a push ([DEPLOY.md](DEPLOY.md#deploy-without-a-push)). |
| `restart_app` | **yes** | A fresh container of the version that runs, without rebuilding or downtime. |
| `rollback_app` | **yes** | The version the app ran before, after the same health check as a deploy. |

### Changing tools ask first

`deploy`, `restart_app` and `rollback_app` require `confirm: true`.
Without it they do nothing and answer, as an error the model sees, what
they would have done and that the user should confirm. Their
annotations say `destructiveHint: true`, so clients that ask before such
calls (Claude Code does, unless you allowed the tool) ask too. Builds run
in the background: the answer is the build id, and `tail_logs` with
`build` and `wait_seconds` waits for it to say Deployed or Failed.

Arguments are checked strictly: an unknown or misspelled argument is an
error the model can correct, not a silent default.

Results are text written for the model. `get_status` and `list_apps`
also return the same data as `structuredContent` JSON; the others do
not, because clients such as Claude Code give the model the JSON instead
of the text when both are there, and their JSON would leave things out. A failure (piway unreachable, a bad token) is a
tool result with `isError: true` and the reason, so the model can tell
you what went wrong.

## Protocol details

- JSON-RPC 2.0; MCP revisions `2025-06-18` (preferred), `2025-03-26` and
  `2024-11-05`. piway answers `initialize` with the client's revision if
  it speaks it, else its newest.
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`. piway has no
  resources or prompts and never sends requests of its own.
- **HTTP** (streamable HTTP transport): `POST /mcp` with one JSON-RPC
  message (`Content-Type: application/json`) answers `200` with the
  response as JSON, or `202` for a notification. No batches, no sessions,
  no event stream (`GET /mcp` is `405`). An unknown
  `MCP-Protocol-Version` header is `400`. Without credentials: `401`, or
  `403` from another machine while no password is set.
- **stdio**: one JSON message per line on stdin, one response per line on
  stdout.
