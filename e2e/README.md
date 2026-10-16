# End-to-end tests

The real `piway` binary, in temporary directories, against real Docker. The
example apps ([`examples/`](../examples)) are built from local git
repositories and deployed by signed webhooks, the way GitHub would send
them: no GitHub is needed.

```sh
make e2e                                    # everything, 20 to 40 minutes
make e2e E2E_RUN='TestE2E/(deploy-loadlab|traffic)$'   # some scenarios
```

It needs Docker, git, and for the dashboard checks `make e2e-deps`
(Playwright and Chromium). CI runs it on `ubuntu-24.04-arm` before every
release (`.github/workflows/e2e.yml`, called by `release.yml`; a failure
stops the release), on pull requests that change it, and by hand
(`workflow_dispatch`).

## Scenarios

They run in this order, against one piway, and later ones use what
earlier ones deployed. `-run 'TestE2E/<name>'` selects them.

| Name | What it proves |
| ---- | -------------- |
| `deploy-<app>` (eight) | Each example deploys from its repository and answers on its stable address, with `run.env` and its secret in the container (its `/api/info`). The static site answers its pages and gives 404 for a missing asset. |
| `volume` | Data written to a volume (#110) is there after a redeploy. |
| `traffic` | Six clients hit an app continuously while it is redeployed: **zero failed requests**. |
| `rollback` | A second version deploys, and a rollback brings the first one back. |
| `secrets` | A secret set through the API shows `restart_needed`, and a restart gives the container the new value (#111). |
| `db-mariadb` | A note in the PHP app is backed up (`piway db backup`), deleted, and restored (`piway db restore`) (#114). |
| `db-redis` | The same for the chat app's history in Redis. |
| `kill9` | `kill -9` of piway leaves the apps running; the next piway serves them again, keeps its builds, and can deploy. |
| `private-repo` | A private repository deploys over SSH with a deploy key (#108). Skipped unless `E2E_PRIVATE_REPO` (`owner/name`, with a `piway.yaml` at its root, on `main`) and `E2E_DEPLOY_KEY` (the private key, with the public half added as a read-only deploy key) are set. In CI they are repository secrets; without them the scenario is skipped, so a run without secrets (a fork's pull request) does not exercise private repositories. |
| `dashboard` | `dashboard/check.mjs`, with Playwright, against the running piway: page changes keep the document (no reload), skeletons show while data loads, numbers glide and jump with reduced motion, and no page shifts its layout (CLS under 0.02) while live data arrives. |

A missing Docker daemon or dashboard dependencies skips the run locally but
fails it when `CI` is set: a gate that skips everything proves nothing.

## Running it on a Pi

The engines (`piway-postgres`, `piway-mariadb`, `piway-redis`) and the
database network have fixed names, so the run refuses a Docker daemon that
already runs piway containers, unless `E2E_FORCE=1`. With it, only the
example apps' names and the engines are touched, and removed at the end (and the run refuses to start if any of those names already exists, rather than delete it);
your own apps are left alone, but the run is heavy: it builds images for
every language, which needs several gigabytes of disk. Run it in phases
(`E2E_RUN`) and remove the images between them on a small card.

On a Pi without system fonts and libraries for Chromium, see
[DEVELOPMENT.md](../docs/DEVELOPMENT.md); `dashboard/check.mjs` itself runs
anywhere `PIWAY_URL=http://127.0.0.1:8080 node check.mjs` can reach a piway.

## Adding a scenario

Add a function to `e2e_test.go` and a `t.Run` in `TestE2E`. `harness_test.go`
has what it needs: `p.repo` makes a git repository, `p.push` sends the signed
webhook, `p.waitBuild` waits for a build and prints its log if it fails,
`p.cli` runs a `piway` subcommand in the same directories, `p.api` calls the
API. Name the subtest without a `/`: Go's `-run` splits patterns on it.
