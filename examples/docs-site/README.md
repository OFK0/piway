# docs-site (static)

This list of examples, as a static site: a Vite build with a small
client-side router. piway builds it in a container on every deploy and
serves the files itself; there is no process.

| Path | What it is |
| ---- | ---------- |
| `/` | The example apps. |
| `/apps/{name}`, `/about` | Client-side routes, drawn in the browser. |
| `/api/info.json` | How the site was built (Vite, Node.js, when), written at build time. |

What it shows about piway:

- `static.build`: `npm ci && npm run build` runs in `node:24-alpine` with the
  repository (here, this directory) mounted; piway copies `static.dir`
  (`dist`) aside and switches the app to it in one step, keeping earlier
  copies for a rollback.
- `static.spa: true`: paths that are not files get `index.html`, so a deep
  link or a reload of `/apps/chat` works.
- The common contract, for a site: there is no process to answer
  `/healthz` or to receive secrets, so `/api/info.json` is a file the build
  writes.

## Deploy

```sh
piway repo add OFK0/piway -path examples/docs-site
```

No secrets; deploy.

## Measured on a Raspberry Pi 4 (4 GB, SD card)

| | |
| --- | --- |
| First deploy | 30 s: pulling `node:24-alpine`, `npm ci` (7 s), `vite build` (0.5 s), copying 5 files (4.8 KiB) |
| Redeploy | 15 s |
| Memory | none of its own: piway serves the files |

Found on the way (#144, fixed): with `spa: true`, a missing asset such as
`/assets/nope.js` also got `index.html` (200, `text/html`) instead of a
404, and the fingerprinted build output had no `Cache-Control`. Now it is a
404, `/about` and `/apps/chat` still get the shell, hashed files are cached
for a year and `index.html` is revalidated.
