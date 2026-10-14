# piway dashboard

React + TypeScript + Vite. Built output goes to `dist/`.

```sh
npm ci
npm run dev     # http://localhost:5173, proxies /api and /healthz to piway
npm run build   # type-check, then bundle into dist/
npm run lint
```

The dev server proxies API calls to `http://localhost:8080`. To use a piway
running elsewhere, set `PIWAY_API`:

```sh
PIWAY_API=http://<pi-address>:8080 npm run dev
```

From the repository root, `make web` runs `npm ci` and the production build.
API types in `src/api.ts` mirror [docs/API.md](../docs/API.md).
