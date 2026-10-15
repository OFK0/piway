# Hardening the Pi

Steps that make the Raspberry Pi safe to run piway on, in the order to
apply them. Written for Raspberry Pi OS / Debian 13 (trixie). Each step
says how to check it; `piway doctor --security` checks them all at once
and prints the fix for anything missing:

```sh
sudo ~/.local/bin/piway doctor --security   # sudo's PATH does not include ~/.local/bin
```

What each step protects against:

| Step | Against |
| ---- | ------- |
| [SSH keys only](#1-ssh-keys-only) | Password guessing and reused passwords |
| [Firewall (ufw)](#2-firewall-ufw) | Services reachable by accident (a debug server, Ollama, a root Docker port) |
| [fail2ban](#3-fail2ban) | Repeated SSH attempts filling logs and using CPU |
| [Automatic security updates](#4-automatic-security-updates) | Known holes left open |
| [The root Docker daemon](#5-the-root-docker-daemon) | Ports that bypass the firewall, a root-owned socket |

piway's own protections are in [API.md](API.md#sign-in) (password,
tokens) and [REMOTE.md](REMOTE.md) (Tailscale, nothing on the internet).

> **Do not lock yourself out.** Keep the SSH session you work in open
> until a *new* session works after each step. If it does not, undo the
> step from the open session. With a keyboard and screen on the Pi, you
> can always fix things locally.

## 1. SSH keys only

A key cannot be guessed, and a phished or reused password stops being
enough.

**On your computer**, create a key if you have none, and copy it to the Pi:

```sh
ssh-keygen -t ed25519            # accept the default path; set a passphrase
ssh-copy-id <user>@<pi>          # asks for your password one last time
ssh -o PasswordAuthentication=no <user>@<pi> true && echo "key works"
```

Go on only after `key works`.

**On the Pi**, turn passwords off in a drop-in file:

```sh
sudo tee /etc/ssh/sshd_config.d/10-hardening.conf <<EOF
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
X11Forwarding no
MaxAuthTries 3
AllowUsers $USER
EOF
sudo sshd -t && sudo systemctl reload ssh
```

The name starts with `10-` on purpose. `sshd_config` includes
`sshd_config.d/*.conf` at its top, in name order, and **the first value
of a setting wins**. Raspberry Pi Imager writes `50-cloud-init.conf`,
which can set `PasswordAuthentication yes`; a file sorting after it, or
an edit further down `sshd_config`, would be ignored.

**Check**, on the Pi and then from your computer:

```sh
sudo sshd -T | grep -E '^(passwordauthentication|kbdinteractiveauthentication|permitrootlogin|x11forwarding|maxauthtries|allowusers) '
ssh -o PubkeyAuthentication=no <user>@<pi>   # must fail: Permission denied (publickey)
```

Lost the key? Log in on the Pi's own keyboard, or put a public key in
`~/.ssh/authorized_keys` from the SD card on another computer.

## 2. Firewall (ufw)

Deny everything that comes in, then allow what piway needs: SSH and
Caddy's app port from the LAN, mDNS so app names resolve, and everything
over Tailscale (which only your devices can reach).

```sh
sudo apt install ufw
LAN=192.168.0.0/24          # your LAN's range: see ip -4 route (the line with wlan0 or eth0)

sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw limit from $LAN to any port 22 proto tcp comment 'SSH from the LAN'
sudo ufw allow in on tailscale0 comment 'everything from my tailnet'
sudo ufw allow from $LAN to any port 8443 proto tcp comment 'Caddy: piway apps (PROXY.md)'
sudo ufw allow from $LAN to any port 5353 proto udp comment 'mDNS: app names'
```

`limit` also rate-limits SSH: more than 6 connections in 30 seconds from
one address are refused. Leave out rules for features you do not use
(8443 without `proxy_domain`, 5353 without a `.local` domain).

Enable it with a safety net: if the rules lock you out, ufw turns itself
off again after 5 minutes.

```sh
sudo systemd-run --unit=ufw-safety --on-active=5min /usr/sbin/ufw disable
sudo ufw enable
# Open a NEW ssh session now. If it works, cancel the safety net:
sudo systemctl stop ufw-safety.timer
```

**Check**:

```sh
sudo ufw status verbose          # Status: active, Default: deny (incoming)
```

From another machine on the LAN, a port nothing should answer, such as
Ollama's `11434`, must time out, and SSH must still work.

piway itself listens on `127.0.0.1` ([REMOTE.md](REMOTE.md)), so it needs
no rule.

## 3. fail2ban

Bans addresses that keep failing SSH logins, at the firewall.

```sh
sudo apt install fail2ban
LAN=192.168.0.0/24          # as in step 2
```

Debian 13's package works as is on Raspberry Pi OS: its
`/etc/fail2ban/jail.d/defaults-debian.conf` enables the `sshd` jail,
reads the systemd journal (Raspberry Pi OS has no `/var/log/auth.log`)
and bans with nftables. Make the bans longer for repeat offenders, and
never ban your own LAN or tailnet:

```sh
sudo tee /etc/fail2ban/jail.d/piway.local <<EOF
[DEFAULT]
bantime = 1h
bantime.increment = true
findtime = 10m
maxretry = 5
ignoreip = 127.0.0.1/8 ::1 $LAN 100.64.0.0/10
EOF
sudo systemctl restart fail2ban
```

`100.64.0.0/10` is Tailscale's address range.

**Check**:

```sh
sudo fail2ban-client status sshd     # Currently banned, Total failed
```

With keys only (step 1), failed attempts cost an attacker nothing, so
fail2ban mostly keeps the journal readable. It still matters if you ever
turn passwords back on.

## 4. Automatic security updates

```sh
sudo apt install unattended-upgrades
sudo dpkg-reconfigure -plow unattended-upgrades    # answer Yes
```

Out of the box it installs Debian's security and point-release updates
only. Add Debian's `trixie-updates` and the Raspberry Pi archive (kernel,
firmware, Raspberry Pi tools), and reboot at night when an update needs
it:

```sh
sudo tee /etc/apt/apt.conf.d/52piway-unattended-upgrades <<'EOF'
Unattended-Upgrade::Origins-Pattern {
        "origin=Debian,codename=${distro_codename}-updates,label=Debian";
        "origin=Raspberry Pi Foundation,codename=${distro_codename},label=Raspberry Pi Foundation";
};
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "04:00";
EOF
```

apt merges the list with the one in `50unattended-upgrades`. With it,
these repositories update by themselves, checked on this Pi with
unattended-upgrades' own matcher:

| Repository | Automatic |
| ---------- | --------- |
| Debian `trixie`, `trixie-security`, `trixie-updates` | Yes |
| Raspberry Pi `archive.raspberrypi.com` | Yes |
| Docker CE (`download.docker.com`) | No: upgrade with `apt`, then `systemctl --user restart docker` |
| Tailscale (`pkgs.tailscale.com`) | No: use `sudo tailscale set --auto-update` |

piway and its apps come back after a reboot on their own: they are user
services with lingering enabled ([DOCKER.md](DOCKER.md)), and piway
serves the running apps again when it starts.

**Check**:

```sh
sudo unattended-upgrade --dry-run --debug 2>&1 | grep -E 'Allowed origins|Packages that will be upgraded'
systemctl list-timers apt-daily-upgrade.timer
```

## 5. The root Docker daemon

piway uses rootless Docker ([DOCKER.md](DOCKER.md)). The root daemon that
the `docker-ce` package starts is not needed, and it is a risk: ports it
publishes go around ufw (Docker writes its own firewall rules), and
anyone in the `docker` group is effectively root.

```sh
sudo systemctl disable --now docker.service docker.socket containerd.service
```

**Check**: `systemctl is-active docker.service` prints `inactive`, and
`systemctl --user is-active docker` (rootless) prints `active`.
