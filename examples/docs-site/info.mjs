// Writes public/api/info.json at build time: a static site has no process
// to answer /api/info, so it says how it was built instead.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'

const vite = JSON.parse(readFileSync('node_modules/vite/package.json', 'utf8')).version
mkdirSync('public/api', { recursive: true })
writeFileSync('public/api/info.json', JSON.stringify({
  app: 'docs-site',
  language: 'Static (Vite)',
  vite,
  node: process.version,
  built: new Date().toISOString(),
  env: { GREETING: process.env.GREETING ?? '' },
}, null, 2) + '\n')
