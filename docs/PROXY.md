# App domains (Caddy)

With `proxy_domain` set, piway publishes each app at
`<name>.<proxy_domain>` through [Caddy](https://caddyserver.com). For
example, with `proxy_domain: local`, the app `photos` is served at
`https://photos.local:8443`, and the name resolves on the LAN over mDNS.

```text
browser ──► Caddy :8443 ──► piway route 127.0.0.1:<port> ──► current container
            (TLS, Host)      (blue/green switch, DEPLOY.md)
```

Caddy terminates TLS and picks the app by host name; piway's own route
for the app (see [DEPLOY.md](DEPLOY.md#routes)) still does the
zero-downtime switch between containers, so Caddy's config only changes
when an app appears or disappears, not on every deploy.

## How piway configures Caddy

piway talks to Caddy's [admin API](https://caddyserver.com/docs/api)
(`caddy_admin`, default `localhost:2019`). It owns one HTTP server in
Caddy's config, `apps.http.servers.piway`, and leaves everything else as
it is:

- one route per app: host `<name>.<proxy_domain>`, proxied to the app's
  stable `127.0.0.1` port;
- a last route that answers `404 No app is served at this name.`;
- `listen` set to `proxy_listen`;
- automatic HTTP-to-HTTPS redirects off, since they need port 80.

Each sync reads Caddy's whole config, rebuilds that server and loads the
result only if it differs. piway syncs at startup, whenever an app gains
or loses a route, and every 30 seconds, so the routes come back on their
own after Caddy restarts or someone edits its config. If Caddy is
unreachable, piway logs it once and keeps retrying; apps stay reachable
on their `127.0.0.1` ports meanwhile.

## Certificates

Caddy decides how to get a certificate for each name:

| Domain | Certificate |
| ------ | ----------- |
| `.local`, `.internal`, `.home.arpa`, `localhost` | From Caddy's own local CA, at once and offline. |
| A public domain whose DNS points at the Pi | From Let's Encrypt over ACME, which needs the Pi reachable from the internet (M5). |

Browsers do not trust Caddy's local CA until you add its root
certificate. Without root, Caddy cannot add it to the system trust store,
so piway turns that attempt off (`install_trust: false`) unless your
Caddy config sets it. Copy the root from the Pi:

```sh
scp pi:.local/share/caddy/pki/authorities/local/root.crt caddy-root.crt
```

and import it on each client: Keychain Access on macOS (set it to Always
Trust), `certmgr.msc` under Trusted Root Certification Authorities on
Windows, or Settings > Privacy and security > Manage certificates in
Chrome. Firefox has its own store (Settings > Certificates). Keep the
root's private key (`root.key` next to it) on the Pi.

`proxy_tls: false` serves plain HTTP instead, with no certificates.

## Names

Something has to make `<name>.<proxy_domain>` resolve to the Pi.

### mDNS (`.local` domains)

With a `proxy_domain` of `local` or one ending in `.local`, piway
publishes each app's name on the LAN through avahi, the mDNS daemon
Raspberry Pi OS runs by default. Each name is an alias (a CNAME record)
of the Pi's own mDNS name, such as `raspberrypi.local`, so devices get
whatever address the Pi has on their network, and a new DHCP lease
needs nothing from piway. A name is published while its app has a
route and withdrawn when piway stops; avahi withdraws it even if piway
is killed.

Prefer `proxy_domain: local`, which gives `photos.local`. Longer names
such as `photos.pi.local` are published too, but not every client
resolves them:

| Client | `photos.local` | `photos.pi.local` |
| ------ | -------------- | ----------------- |
| macOS, iOS | yes | yes |
| Linux with nss-mdns | yes | only with an `/etc/mdns.allow` file and `mdns4` instead of `mdns4_minimal` in `/etc/nsswitch.conf` (see the nss-mdns README) |
| Windows 10 and later | resolves `.local` names over mDNS; not tested with piway | not tested |
| Android | depends on the version and browser; use a hosts file or DNS if it fails | same |

piway skips a name that is the Pi's own (an app called `raspberrypi`
with `proxy_domain: local`) and logs why. avahi probes each name before
it answers for it: if another device already answers to `photos.local`,
that name is not published, piway logs it once, and the other apps are
unaffected. Rename the app or change `proxy_domain`.

If avahi is not running, piway logs that app names are not published
and serves the apps anyway.

### Hosts file

For other domains, or clients without mDNS, add the names to each
client's hosts file (`/etc/hosts`, or
`C:\Windows\System32\drivers\etc\hosts`), using the Pi's LAN address:

```text
192.0.2.10  photos.pi.internal
```

## Ports

A user cannot bind ports below 1024, so Caddy serves apps on `:8443` by
default. To serve them on 443 (and drop `:8443` from the URLs), allow
unprivileged ports once, then set `proxy_listen: ":443"`:

```sh
echo 'net.ipv4.ip_unprivileged_port_start=443' | sudo tee /etc/sysctl.d/50-unprivileged-ports.conf
sudo sysctl --system
```

This lets every user bind 443 and up. Rootless Docker's own docs suggest
the same setting.

## Install Caddy (no sudo)

Caddy is a single binary. Download the `linux_arm64` archive of the
[latest release](https://github.com/caddyserver/caddy/releases) and check
it against the release's checksums file:

```sh
V=2.11.7
curl -fLO https://github.com/caddyserver/caddy/releases/download/v$V/caddy_${V}_linux_arm64.tar.gz
curl -fLO https://github.com/caddyserver/caddy/releases/download/v$V/caddy_${V}_checksums.txt
grep linux_arm64.tar.gz caddy_${V}_checksums.txt | sha512sum -c -
tar xzf caddy_${V}_linux_arm64.tar.gz caddy
install -m 755 caddy ~/.local/bin/caddy
```

Run it as a user service, next to piway's
([`deploy/systemd/caddy.service`](../deploy/systemd/caddy.service)):

```sh
cp deploy/systemd/caddy.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now caddy
```

`caddy run --resume` starts from the config Caddy saved last, so the apps
are served again before piway syncs. Then set the domain in piway's
config and restart it:

```yaml
proxy_domain: local
```

```console
$ curl --cacert caddy-root.crt https://photos.local:8443/
```

## Security

- Caddy's admin API has no authentication. It listens on `localhost`
  only, but any process on the Pi can reconfigure Caddy through it.
- Caddy listens on every interface: published apps are reachable from the
  LAN, which is the point. piway's dashboard and API are not behind Caddy;
  they need a password ([API.md](API.md#sign-in)).
