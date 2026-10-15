# ADR 0001: Remote access with Tailscale

- Status: accepted
- Date: 2026-10-15
- Issue: #27

## Context

piway runs on a Raspberry Pi at home, behind a router. Its owner wants
the dashboard (and later Web Push notifications, #30, and the MCP server,
M6) from anywhere, without opening router ports. Web Push needs HTTPS
with a certificate browsers trust.

The dashboard and API now need a password or token (#28), but every
exposure still adds attack surface: the fewer machines that can reach
piway at all, the better.

## Options

| | Tailscale (`tailscale serve`) | Cloudflare Tunnel | Port forward + Caddy (ACME) |
| --- | --- | --- | --- |
| Model | Overlay network (WireGuard mesh) | Outbound tunnel to Cloudflare's edge | The Pi is on the internet |
| Who can reach piway | Devices signed in to the owner's tailnet | Anyone on the internet (unless Cloudflare Access is added) | Anyone on the internet |
| Router changes | None | None | Port forward, and a static IP or dynamic DNS |
| HTTPS | Let's Encrypt certificate for `<host>.<tailnet>.ts.net`, issued and renewed by Tailscale | Cloudflare's certificate at the edge | Caddy over ACME |
| Needs | Tailscale app on each client device | A domain on Cloudflare; TLS ends at Cloudflare, which sees all traffic | A domain; CGNAT ISPs make it impossible |
| Cost | Free personal plan | Free plan | Domain |
| Already on the Pi | Yes | No | No |

## Decision

Use Tailscale: `tailscale serve` publishes piway's `127.0.0.1:8080` as
`https://<host>.<tailnet>.ts.net` to the tailnet only.

- Nothing is reachable from the internet: an attacker needs a device in
  the tailnet before piway's password is even in play.
- No router configuration, and it works behind CGNAT.
- Real HTTPS with no domain, which Web Push (#30) requires.
- piway keeps listening on `127.0.0.1`; Tailscale is the only way in from
  outside, and an SSH tunnel stays the fallback.

## Consequences

- Each client (laptop, phone) runs the Tailscale app and signs in to the
  tailnet. That is the price of not being on the internet.
- `tailscale serve` connects to piway from loopback. piway must not treat
  such requests as local: it recognizes forwarded requests by their proxy
  headers, counts wrong passwords per forwarded client and marks the
  session cookie `Secure` behind the proxy's HTTPS (`internal/auth`).
- GitHub cannot reach the Pi, so push webhooks (#22) still do not arrive.
  `tailscale funnel` could expose just the webhook path to the internet;
  that is a separate decision.
- Apps published through Caddy (#24) stay LAN-only; serving them over the
  tailnet is a later step.
- The Pi's name and the tailnet's name appear in certificate transparency
  logs, as for any publicly trusted certificate.
