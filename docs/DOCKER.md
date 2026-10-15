# Docker on the Pi

piway runs apps as Docker containers. It uses **rootless Docker**: the
Docker daemon runs as your user, not as root.

## Why rootless

Whoever can talk to a Docker daemon can start a container that mounts `/`,
so access to a root daemon's socket is root on the machine. piway needs that
access, and from M5 on it is reachable from the internet. With rootless
Docker, the daemon, its socket and every container run as your user:
a compromised piway, or a container escape, gets your user's rights, not
root's.

The trade-offs are small for piway: containers cannot bind ports below
1024 (Caddy, not the apps, faces clients: [PROXY.md](PROXY.md)), and networking goes through
a user-space stack that is somewhat slower than the kernel bridge.

## Install (once, needs sudo)

Tested on Raspberry Pi OS / Debian 13 (trixie), arm64. Either Debian's
`docker.io` (Docker 26) or Docker CE from Docker's own repository (Docker
29, `docker-ce` and `docker-ce-rootless-extras`) works; piway speaks API
1.44, which both accept.

```sh
# 1. Packages: Docker, the rootless helpers, and user-session D-Bus.
#    Debian's packages:
sudo apt install docker.io rootlesskit uidmap slirp4netns fuse-overlayfs dbus-user-session
#    or, with Docker CE already installed from download.docker.com:
sudo apt install uidmap slirp4netns dbus-user-session

# 2. Turn off the root daemon that the package starts.
sudo systemctl disable --now docker.service docker.socket

# 3. Keep your user services (Docker, piway) running without a login
#    session, and start them at boot.
sudo loginctl enable-linger "$USER"
```

Your user needs subordinate id ranges; Raspberry Pi OS creates them:

```sh
grep "^$USER:" /etc/subuid /etc/subgid   # e.g. ofk:100000:65536 in both
```

## Set up rootless Docker (as your user, no sudo)

```sh
dockerd-rootless-setuptool.sh check     # lists anything still missing
dockerd-rootless-setuptool.sh install   # Docker CE: on $PATH
# Debian docker.io: /usr/share/docker.io/contrib/dockerd-rootless-setuptool.sh install
systemctl --user enable --now docker
```

The daemon's socket is `$XDG_RUNTIME_DIR/docker.sock`
(`/run/user/1000/docker.sock`). Docker CE's setup tool also creates and
selects a CLI context named `rootless`, so `docker` already talks to it;
otherwise point the CLI at the socket:

```sh
echo 'export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/docker.sock' >> ~/.bashrc
export DOCKER_HOST=unix://$XDG_RUNTIME_DIR/docker.sock
docker run --rm hello-world
```

piway finds the socket the same way: `$DOCKER_HOST` if set, else
`$XDG_RUNTIME_DIR/docker.sock`.

The rootless daemon can run next to a system (root) daemon: they use
different sockets and data directories (`~/.local/share/docker`), and
piway only talks to the rootless one. Turning the system daemon off saves
memory and disk on the Pi if nothing else uses it.

## Memory limits (optional, needs a reboot)

Raspberry Pi OS boots with `cgroup_disable=memory`, so the memory cgroup
controller is off and containers cannot have memory limits. A runaway app
could then use all 4 GB. To enable it, append `cgroup_enable=memory` to the
single line in `/boot/firmware/cmdline.txt` and reboot:

```sh
sudo sed -i '1 s/$/ cgroup_enable=memory/' /boot/firmware/cmdline.txt
sudo reboot
```

Afterwards `cat /sys/fs/cgroup/cgroup.controllers` lists `memory`, and
`docker info` no longer warns about missing memory limit support.

## Check

```sh
docker info --format '{{.SecurityOptions}}'   # includes name=rootless
docker info --format '{{.CgroupVersion}} {{.CgroupDriver}}'   # 2 systemd

# piway's client against the real daemon: pulls busybox, runs, stops, removes.
PIWAY_DOCKER_TEST=1 go test ./internal/docker -run RealDaemon -v
```
