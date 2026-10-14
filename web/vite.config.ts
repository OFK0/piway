import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// In development the dashboard runs on Vite's server (with hot reload) and
// API calls are proxied to a running piway, so the browser sees one origin.
// That matters for /api/v1/stream: piway rejects cross-origin WebSockets.
// Point PIWAY_API elsewhere to develop against another machine.
const api = process.env.PIWAY_API ?? 'http://localhost:8080'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
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
