import fs from 'node:fs'
import path from 'node:path'
import type { Plugin } from 'vite'

// Emits THIRD_PARTY_NOTICES.txt next to the build: the license of every npm
// package whose code ends up in the bundle. MIT, ISC, BSD and OFL all ask
// for their text to travel with binary copies, and the Go binary embeds
// this build. The Makefile prepends the Go modules and standard library.
//
// The list is derived from the bundle itself, so it cannot go stale. A
// bundled package without a license file fails the build.

interface Options {
  fileName: string
  /** Packages that reach the build without JS imports (fonts via CSS). */
  extraPackages: string[]
  /**
   * For packages that ship no license file at their root: license texts
   * kept in this repository (web/licenses/), plus files inside the package
   * to append, such as the licenses of code it vendors.
   */
  overrides?: Record<string, { license: string; vendored?: string[] }>
}

const licenseFile = /^(licen[cs]e|copying)(\.(md|txt))?$/i

export function thirdPartyNotices(opts: Options): Plugin {
  return {
    name: 'piway-third-party-notices',
    apply: 'build',
    generateBundle(_, bundle) {
      const dirs = new Map<string, string>() // package dir -> package name
      for (const out of Object.values(bundle)) {
        if (out.type !== 'chunk') continue
        for (const id of Object.keys(out.modules)) {
          const dir = packageDir(id)
          if (dir) dirs.set(dir, '')
        }
      }
      for (const name of opts.extraPackages) {
        dirs.set(path.resolve('node_modules', name), '')
      }

      const entries: string[] = []
      const missing: string[] = []
      const seen = new Set<string>()
      for (const dir of dirs.keys()) {
        const pkg = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'))
        const key = `${pkg.name}@${pkg.version}`
        if (seen.has(key)) continue
        seen.add(key)
        const override = opts.overrides?.[pkg.name]
        const file = fs.readdirSync(dir).find((f) => licenseFile.test(f))
        let text: string
        if (override) {
          text = fs.readFileSync(override.license, 'utf8').trim()
          for (const rel of override.vendored ?? []) {
            text += `\n\n--- ${rel} ---\n\n${fs.readFileSync(path.join(dir, rel), 'utf8').trim()}`
          }
        } else if (file) {
          text = fs.readFileSync(path.join(dir, file), 'utf8').trim()
        } else {
          missing.push(key)
          continue
        }
        entries.push(`${'='.repeat(78)}\n${pkg.name} ${pkg.version} (${pkg.license ?? 'see below'})\n${'='.repeat(78)}\n\n${text}\n`)
      }
      if (missing.length > 0) {
        this.error(`no license file in bundled packages: ${missing.join(', ')}`)
      }

      // Sorted by code point, not locale, so the file (and the release
      // archive) is byte-identical on every machine.
      const title = (e: string) => e.split('\n')[1]
      entries.sort((a, b) => (title(a) < title(b) ? -1 : title(a) > title(b) ? 1 : 0))
      this.emitFile({
        type: 'asset',
        fileName: opts.fileName,
        source: `Dashboard (web/): ${entries.length} packages\n\n${entries.join('\n')}`,
      })
    },
  }
}

// /x/node_modules/@scope/pkg/dist/a.js -> /x/node_modules/@scope/pkg
function packageDir(id: string): string | null {
  const clean = id.replace(/^\0/, '').split('?')[0]
  const i = clean.lastIndexOf('/node_modules/')
  if (i < 0) return null
  const rest = clean.slice(i + '/node_modules/'.length).split('/')
  const name = rest[0].startsWith('@') ? `${rest[0]}/${rest[1]}` : rest[0]
  return clean.slice(0, i + '/node_modules/'.length) + name
}
