# Documentation site

The site at <https://ofk0.github.io/piway/>, built with
[VitePress](https://vitepress.dev) from the Markdown in [`../docs`](../docs):
GitHub and the site show the same files. This folder holds only the site's
configuration (`.vitepress/config.mts`: navigation, sidebar, link handling)
and theme (`.vitepress/theme`: piway's colors and fonts).

```sh
cd site
npm ci
npm run dev       # http://localhost:5173/piway/, reloads on edits in docs/
npm run build     # .vitepress/dist; fails on a broken link between pages
```

- A new page in `docs/` also needs an entry in the sidebar in `config.mts`.
- Links to files outside `docs/` (such as `../deploy/config.example.yaml`)
  are turned into links to the file on GitHub.
- The build adds `install.sh` and the favicon, so the installer is also at
  <https://ofk0.github.io/piway/install.sh>.
- `.github/workflows/docs.yml` builds every pull request that touches the
  docs and publishes `main`.
