import fs from 'node:fs'
import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

import { thirdPartyNotices } from './vite-plugin-notices.ts'

// In development the dashboard runs on Vite's server (with hot reload) and
// API calls are proxied to a running piway, so the browser sees one origin.
// That matters for /api/v1/stream: piway rejects cross-origin WebSockets.
// Point PIWAY_API elsewhere to develop against another machine.
const api = process.env.PIWAY_API ?? 'http://localhost:8080'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    thirdPartyNotices({
      // The Makefile merges this with the Go licenses into THIRD_PARTY_NOTICES.txt.
      fileName: 'THIRD_PARTY_NOTICES.web.txt',
      extraPackages: ['@fontsource-variable/ibm-plex-sans', '@fontsource/ibm-plex-mono'],
      overrides: {
        // Recharts' copy of d3. MIT text from github.com/FormidableLabs/victory;
        // the d3 modules it vendors keep their own ISC licenses.
        'victory-vendor': {
          license: 'licenses/victory-vendor.LICENSE',
          vendored: fs
            .readdirSync('node_modules/victory-vendor/lib-vendor')
            .sort()
            .map((d) => `lib-vendor/${d}/LICENSE`),
        },
      },
    }),
  ],
  resolve: {
    alias: { '@': path.resolve(import.meta.dirname, './src') },
  },
  // The Go binary embeds this directory (internal/webui, -tags embedui).
  // It lives outside web/ because go:embed cannot reach parent directories.
  build: {
    outDir: path.resolve(import.meta.dirname, '../internal/webui/dist'),
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': { target: api, ws: true },
      '/healthz': api,
    },
  },
})
