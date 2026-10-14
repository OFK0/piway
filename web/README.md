# piway dashboard

React + TypeScript + Vite, with shadcn/ui (Base UI) and Tailwind CSS v4.
Built output goes to `../internal/webui/dist/`, which the Go binary embeds
(`make build`, build tag `embedui`).

## Design system

[DESIGN.md](DESIGN.md) is the spec: colors, type, shape, states, charts and
the do's and don'ts. Tokens live in `src/index.css`, components in
`src/components/ui`. The rendered reference is at `/design-system`
(link in the footer).

Add a component with `npx shadcn@latest add <name>`, then restyle it to the
spec and show it on the design system page.

```sh
npm ci
npm run dev     # http://localhost:5173, proxies /api and /healthz to piway
npm run build   # type-check, then bundle into ../internal/webui/dist/
npm run lint
```

The dev server proxies API calls to `http://localhost:8080`. To use a piway
running elsewhere, set `PIWAY_API`:

```sh
PIWAY_API=http://<pi-address>:8080 npm run dev
```

From the repository root, `make web` runs `npm ci` and the production build.
API types in `src/api.ts` mirror [docs/API.md](../docs/API.md).
