# Releasing

A release is a git tag. Pushing `vMAJOR.MINOR.PATCH` (optionally with a
`-pre` suffix, such as `v0.2.0-rc.1`) runs `.github/workflows/release.yml`,
which publishes a GitHub release with:

| File | Contents |
| ---- | -------- |
| `piway_linux_arm64.tar.gz` | `piway` (dashboard embedded), `LICENSE`, `THIRD_PARTY_NOTICES.txt`, `README.md`, `piway.service`, `config.example.yaml` |
| `checksums.txt` | SHA-256 of the archive |

Tags with a suffix are published as pre-releases.

## Cutting a release

```sh
git checkout main && git pull
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

The end-to-end suite ([e2e/README.md](../e2e/README.md)) is not part of the
release: it takes 20 to 40 minutes. Run it before tagging when the release
touches deploys, the router or the dashboard: `make e2e` on a machine with
Docker, or **e2e**, *Run workflow* in the Actions tab.

The workflow, on a native arm64 runner:

1. Checks that the tag is a version and points at a commit on `main`.
2. Runs every check: `make lint test-race web-lint web-test`.
3. Builds with `make dist VERSION=<tag>`.
4. Verifies the archive against its checksum and runs the binary from it.
   `piway -version` must print exactly `piway <tag> (<commit>)`, `/healthz`
   must report the tag, `/` must serve the dashboard, and the served
   `/THIRD_PARTY_NOTICES.txt` must match the one in the archive.
5. Records a signed build provenance attestation for the archive.
6. Creates the release with notes generated from the merged pull requests.

If any step fails, nothing is published. Fix the problem on `main`, delete
the tag (`git push --delete origin v0.2.0 && git tag -d v0.2.0`) and tag again.

Pull requests that change `release.yml` or the `Makefile` run steps 2–4 as
a dry run with version `v0.0.0-dryrun`, so the pipeline is tested before a
real tag needs it. The dry run leaves the end-to-end suite out.

## Reproducible archives

`make dist` produces the same bytes from the same commit, with the same Go
and Node versions, on any machine: file times come from the commit,
owners and permissions are normalized, files are sorted, gzip stores no name
or time, and `-trimpath` keeps local paths out of the binary. To check a
release, build its tag and compare:

```sh
git checkout v0.2.0
make dist VERSION=v0.2.0
cat dist/checksums.txt   # same hash as the release's checksums.txt
```

## Installing on the Pi

`install.sh` and `piway setup` do all of this ([INSTALL.md](INSTALL.md)).
The steps below are what they run, for installing by hand.

```sh
V=v0.2.0
curl -fsSLO https://github.com/OFK0/piway/releases/download/$V/piway_linux_arm64.tar.gz
curl -fsSLO https://github.com/OFK0/piway/releases/download/$V/checksums.txt
sha256sum -c checksums.txt          # must print: piway_linux_arm64.tar.gz: OK

# Optional: prove it was built by this repository's release workflow.
gh attestation verify piway_linux_arm64.tar.gz --repo OFK0/piway

tar -xzf piway_linux_arm64.tar.gz
install -m755 piway_linux_arm64/piway ~/.local/bin/piway
piway -version
```

First install as a user service:

```sh
mkdir -p ~/.config/systemd/user ~/.config/piway
cp piway_linux_arm64/piway.service ~/.config/systemd/user/
cp -n piway_linux_arm64/config.example.yaml ~/.config/piway/config.yaml
systemctl --user daemon-reload && systemctl --user enable --now piway
```

To upgrade, replace the binary and run `systemctl --user restart piway`.
Open dashboards reconnect on their own.

## Versioning

Until `v1.0.0`, a minor version (`v0.3.0`) may change the HTTP API, config
keys or flags; the release notes call these out (Conventional Commits with
`!` or `BREAKING CHANGE:`). Patch versions only fix bugs.
