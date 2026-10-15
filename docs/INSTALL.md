# Installing piway

piway runs on a Raspberry Pi 4 (tested; other 64-bit Pis should work) with **Raspberry Pi OS (64-bit)**,
Desktop or Lite, based on Debian 12 (bookworm) or 13 (trixie). Flash it
with Raspberry Pi Imager, sign in over SSH, and run:

```sh
curl -fsSL https://raw.githubusercontent.com/OFK0/piway/main/install.sh | sh
```

The script downloads the latest release, checks its SHA-256 checksum,
installs `piway` in `~/.local/bin` and starts `piway setup`. Setup asks
three questions, shows its plan, and asks before each step:

```text
Publish apps at <name>.local on your network (Caddy and mDNS)? [Y/n]
Install Docker, to build and run apps? [Y/n]
Reach the dashboard from your other devices with Tailscale? [y/N]
```

When it finishes, it checks the result with `piway doctor` and prints the
dashboard's address. If it turned on memory limits for containers, reboot
once (`sudo reboot`).

## What setup does

Only what is missing, in this order. Each step is the one the linked guide
describes, and every command that runs with `sudo` is printed before it
runs.

| Step | When | Guide |
| ---- | ---- | ----- |
| Put `~/.local/bin` on your `PATH` | if it is not | |
| Write `~/.config/piway/config.yaml` | if there is none; an existing file is never changed | [CONFIG.md](CONFIG.md) |
| Set the dashboard password | if none is set | [CONFIG.md](CONFIG.md#dashboard-password) |
| Turn on lingering (`sudo loginctl enable-linger`) | so piway runs at boot and without a login | |
| Install Docker from Docker's repository, turn off the root daemon, start rootless Docker | with Docker | [DOCKER.md](DOCKER.md) |
| Add `cgroup_enable=memory` to `/boot/firmware/cmdline.txt` | if memory limits are off; needs a reboot | [DOCKER.md](DOCKER.md) |
| Install Caddy (checksum verified) and run it as a user service | with a proxy domain | [PROXY.md](PROXY.md) |
| Allow ports below 1024 for Caddy | if `proxy_listen` is one | [PROXY.md](PROXY.md) |
| Run avahi | for `.local` names, if it is not running | [PROXY.md](PROXY.md#mdns-local-domains) |
| Install and start the piway user service, then wait for it to answer | | [RELEASING.md](RELEASING.md#installing-on-the-pi) |
| Install Tailscale and `tailscale serve` piway | with Tailscale | [REMOTE.md](REMOTE.md) |

Setup is safe to run again at any time: it checks first and does only what
is still missing. If a step fails, fix what it reports and run it again.

## Options

```sh
piway setup --dry-run           # print the plan and every command; change nothing
piway setup --yes               # use the defaults, ask nothing
piway setup --proxy-domain ""   # no Caddy: apps only on their 127.0.0.1 ports
piway setup --no-docker         # no Docker: metrics only, no deploys
piway setup --tailscale         # also set up remote access
echo 'secret' | piway setup --yes --password-stdin   # unattended, with a password
```

With `install.sh`, pass them after `sh -s --`:

```sh
curl -fsSL https://raw.githubusercontent.com/OFK0/piway/main/install.sh | sh -s -- --dry-run
```

`install.sh` itself takes `--version v0.2.0` (a specific release) and
`--no-setup` (install the binary only).

Some things only you can do; setup says when:

- **Tailscale**: `sudo tailscale up` prints a link to sign the Pi in, and
  MagicDNS and HTTPS certificates are turned on once in the Tailscale admin
  console.
- **Rebooting** for memory limits.

## Checking an installation

```sh
piway doctor                                # what works, what does not, and the fix
sudo ~/.local/bin/piway doctor --security   # the hardening steps (HARDENING.md)
```

## Uninstalling

```sh
piway uninstall            # stops and removes the services and binaries
piway uninstall --purge    # also deletes the config, password, tokens and history
```

Docker, avahi and lingering stay, since other programs may use them;
uninstall prints how to remove them.

## Installing by hand

Every step above is also documented on its own: start with
[RELEASING.md](RELEASING.md#installing-on-the-pi), then
[DOCKER.md](DOCKER.md), [PROXY.md](PROXY.md) and [REMOTE.md](REMOTE.md).
