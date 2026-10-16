# Remote access

Reach the dashboard from anywhere, over HTTPS, without opening router
ports: [Tailscale](https://tailscale.com) publishes piway to your own
devices only. Why Tailscale: [ADR 0001](adr/0001-remote-access-with-tailscale.md).

```text
phone / laptop ──(tailnet, WireGuard)──► Pi: tailscale serve :443 ──► piway 127.0.0.1:8080
```

## Before you start

- **Set a dashboard password** (`piway -set-password`,
  [CONFIG.md](CONFIG.md#dashboard-password)). Without one, piway refuses
  requests that come through Tailscale, since they are not from the Pi
  itself.
- Keep piway on `addr: "127.0.0.1:8080"`, so Tailscale is the only way in
  from other machines.

## Set up (once)

1. **Tailscale on the Pi**, signed in ([tailscale.com/download](https://tailscale.com/download)):

   ```sh
   sudo tailscale up
   ```

2. **Turn on HTTPS certificates** for your tailnet: in the admin console,
   DNS → MagicDNS on, then HTTPS Certificates → Enable.

3. **Let your user manage `tailscale serve`** without sudo:

   ```sh
   sudo tailscale set --operator=$USER
   ```

4. **Publish piway**:

   ```sh
   tailscale serve --bg 8080
   tailscale serve status
   ```

   The first request may take a few seconds while Tailscale gets the
   certificate. `--bg` keeps the configuration across reboots.

5. **On each device**: install Tailscale, sign in to the same tailnet, and
   open `https://<pi>.<tailnet>.ts.net` (shown by `tailscale serve
   status`).

To stop publishing: `tailscale serve reset`.

## What piway does behind Tailscale

`tailscale serve` connects to piway from `127.0.0.1` and adds
`X-Forwarded-For`, `X-Forwarded-Proto` and `Tailscale-User-*` headers.
piway trusts those headers only on loopback connections:

- A forwarded request is never "from the Pi itself", so the no-password
  exception does not apply to it.
- Wrong passwords are counted per device (its tailnet address), not for
  everyone at once.
- The session cookie is `Secure`, since the browser uses HTTPS.

The same applies to any other reverse proxy on the Pi that sets these
headers, such as Caddy.

## Not covered yet

- **GitHub webhooks** cannot reach the Pi through the tailnet. Deploys
  from `git push` need a public path to `/api/v1/webhooks/github` only,
  for example `tailscale funnel` on its own port; piway finds that address
  and shows it when you add a repository
  ([DEPLOY.md](DEPLOY.md#3-add-the-webhook-on-github)).
- **Apps** published through Caddy ([PROXY.md](PROXY.md)) are reachable
  on the LAN, not over the tailnet.
