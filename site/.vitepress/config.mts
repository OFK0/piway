import path from 'node:path'
import { defineConfig } from 'vitepress'

// The site is built from ../docs, the same Markdown that GitHub shows, so
// there is one copy of every page. Links that leave docs/ (an example
// config, a unit file) point to the file on GitHub instead.
const repo = 'https://github.com/OFK0/piway'
const docs = path.resolve(import.meta.dirname, '../../docs')

export default defineConfig({
  title: 'piway',
  description: 'A metrics dashboard and git-push deploys for your Raspberry Pi.',
  base: '/piway/', // served at https://ofk0.github.io/piway/
  srcDir: docs,
  srcExclude: ['brand/**'],
  cleanUrls: true,
  // Broken links between pages fail the build.
  ignoreDeadLinks: false,
  head: [['link', { rel: 'icon', type: 'image/svg+xml', href: '/piway/favicon.svg' }]],

  vite: {
    // The logos are served straight from docs/brand, not copied; the build
    // adds the dashboard's favicon and install.sh (package.json).
    publicDir: path.join(docs, 'brand'),
    // Pages live outside site/, so their imports would be looked up from
    // docs/; resolve Vue from the site's own packages.
    resolve: {
      alias: [{ find: /^vue($|\/)/, replacement: path.resolve(import.meta.dirname, '../node_modules/vue') + '$1' }],
    },
    server: { fs: { allow: [docs, import.meta.dirname, path.resolve(import.meta.dirname, '..')] } },
  },

  markdown: {
    config(md) {
      const render = md.renderer.rules.link_open ?? ((tokens, i, opts, _env, self) => self.renderToken(tokens, i, opts))
      md.renderer.rules.link_open = (tokens, i, opts, env, self) => {
        const href = tokens[i].attrGet('href')
        if (href?.startsWith('../')) {
          // Relative to the page's file in docs/; resolved against the repository root.
          const file = path.posix.normalize(path.posix.join('docs', path.posix.dirname(env.relativePath ?? ''), href))
          tokens[i].attrSet('href', `${repo}/blob/main/${file}`)
        }
        return render(tokens, i, opts, env, self)
      }
    },
  },

  themeConfig: {
    logo: { light: '/piway-logo-black.svg', dark: '/piway-logo-white.svg', alt: 'piway' },
    siteTitle: false, // the logo is the wordmark
    nav: [
      { text: 'Install', link: '/INSTALL' },
      { text: 'Deploy an app', link: '/DEPLOY' },
      { text: 'Reference', link: '/CONFIG' },
    ],
    sidebar: [
      {
        text: 'Getting started',
        items: [
          { text: 'Install', link: '/INSTALL' },
          { text: 'Deploy an app', link: '/DEPLOY' },
          { text: 'App manifest (piway.yaml)', link: '/MANIFEST' },
        ],
      },
      {
        text: 'Guides',
        items: [
          { text: 'Docker', link: '/DOCKER' },
          { text: 'Publishing apps (Caddy)', link: '/PROXY' },
          { text: 'Remote access', link: '/REMOTE' },
          { text: 'Hardening the Pi', link: '/HARDENING' },
          { text: 'Alerts and notifications', link: '/ALERTS' },
          { text: 'Claude Code (MCP)', link: '/MCP' },
        ],
      },
      {
        text: 'Reference',
        items: [
          { text: 'Configuration', link: '/CONFIG' },
          { text: 'HTTP API and metrics', link: '/API' },
          { text: 'Storage', link: '/STORAGE' },
          { text: 'Development', link: '/DEVELOPMENT' },
          { text: 'Releasing', link: '/RELEASING' },
          { text: 'Roadmap', link: '/ROADMAP' },
          { text: 'ADR 0001: Tailscale', link: '/adr/0001-remote-access-with-tailscale' },
        ],
      },
    ],
    socialLinks: [{ icon: 'github', link: repo }],
    editLink: { pattern: `${repo}/edit/main/docs/:path`, text: 'Edit this page on GitHub' },
    search: { provider: 'local' },
    footer: { message: 'Released under the MIT license.' },
  },
})
