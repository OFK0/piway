// Dashboard checks against a running piway (PIWAY_URL), with Playwright:
// pages change without reloading, skeletons show while data loads, numbers
// glide (and do not, with reduced motion), and nothing shifts the layout.
// Prints one line per check and exits 1 if any failed.
import { chromium } from 'playwright'

const base = process.env.PIWAY_URL ?? 'http://127.0.0.1:8080'
const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ': ' + detail : ''}`)
  if (!ok) failures.push(name)
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const browser = await chromium.launch()

// Starts recording layout shifts (not caused by input) before the page runs.
const recordShifts = (page) =>
  page.addInitScript(() => {
    window.__cls = 0
    window.__shifts = []
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) {
        if (e.hadRecentInput) continue
        window.__cls += e.value
        // Which elements moved, and from where to where, to find the culprit.
        window.__shifts.push({
          value: e.value,
          at: Math.round(e.startTime),
          nodes: e.sources.map((s) => {
            const n = s.node
            const name = n ? `${n.nodeName.toLowerCase()}${n.className && typeof n.className === 'string' ? '.' + n.className.split(' ')[0] : ''}` : '?'
            const text = n ? (n.textContent || '').replace(/\s+/g, ' ').slice(0, 24) : ''
            return `${name} "${text}" ${Math.round(s.previousRect.height)}px -> ${Math.round(s.currentRect.height)}px, y ${Math.round(s.previousRect.y)} -> ${Math.round(s.currentRect.y)}`
          }),
        })
      }
    }).observe({ type: 'layout-shift', buffered: true })
  })

// The page is ready when its heading is there and nothing is a skeleton.
const settled = async (page) => {
  await page.locator('h1').first().waitFor({ timeout: 20000 })
  await page.waitForFunction(() => document.querySelectorAll('[data-slot=skeleton]').length === 0, null, { timeout: 20000 })
}

try {
  // 1. Page changes do not reload the document (#87): a marker on window
  //    survives clicks through every page of the navigation.
  {
    const page = await browser.newPage()
    await page.goto(base + '/')
    await settled(page)
    await page.evaluate(() => (window.__marker = 'same document'))
    const seen = []
    for (const [link, heading] of [['Apps', 'Apps'], ['Databases', 'Databases'], ['Settings', 'Settings'], ['Overview', null]]) {
      await page.getByRole('navigation').getByRole('link', { name: link }).first().click()
      if (heading) await page.getByRole('heading', { level: 1, name: heading }).waitFor({ timeout: 10000 })
      else await page.waitForURL(base + '/')
      seen.push(await page.evaluate(() => window.__marker))
    }
    check('page changes keep the document', seen.every((m) => m === 'same document'), seen.join(', '))
    await page.close()
  }

  // 2. Skeletons while data loads: slow the apps list down and look.
  {
    const page = await browser.newPage()
    await page.route('**/api/v1/apps', async (route) => {
      await sleep(1500)
      await route.continue()
    })
    await page.goto(base + '/apps')
    const skeleton = await page.locator('[data-slot=skeleton]').first().waitFor({ timeout: 1200 }).then(() => true, () => false)
    check('a skeleton shows while the apps load', skeleton)
    await page.waitForFunction(() => document.querySelectorAll('[data-slot=skeleton]').length === 0, null, { timeout: 15000 })
    check('and is gone once they have', true)
    await page.close()
  }

  // 3. Numbers glide to new values: in some second, one number takes
  //    several intermediate values. With reduced motion it jumps.
  const motion = async (reduced) => {
    const ctx = await browser.newContext({ reducedMotion: reduced ? 'reduce' : 'no-preference' })
    const page = await ctx.newPage()
    await page.goto(base + '/')
    await settled(page)
    // Sample every number on screen each animation frame for 8 s, and
    // report the most distinct values any one number showed in 700 ms.
    const best = await page.evaluate(
      () =>
        new Promise((resolve) => {
          const els = [...document.querySelectorAll('main .tabular-nums')]
          const seen = els.map(() => [])
          const t0 = performance.now()
          const frame = () => {
            const now = performance.now()
            els.forEach((el, i) => {
              const last = seen[i].at(-1)
              if (!last || last.v !== el.textContent) seen[i].push({ v: el.textContent, t: now })
            })
            if (now - t0 < 8000) requestAnimationFrame(frame)
            else {
              let best = 0
              for (const s of seen)
                for (let a = 0; a < s.length; a++) {
                  const inWindow = s.filter((x) => x.t >= s[a].t && x.t < s[a].t + 700)
                  best = Math.max(best, new Set(inWindow.map((x) => x.v)).size)
                }
              resolve({ best, numbers: els.length })
            }
          }
          requestAnimationFrame(frame)
        }),
    )
    await ctx.close()
    return best
  }
  {
    const m = await motion(false)
    check('numbers glide between values', m.numbers > 0 && m.best >= 4, `${m.numbers} numbers, up to ${m.best} values in 700 ms`)
    const r = await motion(true)
    check('numbers jump with reduced motion', r.best <= 2, `up to ${r.best} values in 700 ms`)
  }

  // 4. No layout shift on any page while live data arrives.
  for (const path of ['/', '/apps', '/apps/loadlab', '/databases', '/settings']) {
    const page = await browser.newPage({ viewport: { width: 1200, height: 900 } })
    await recordShifts(page)
    await page.goto(base + path)
    await settled(page)
    await sleep(5000) // live numbers, polls and charts keep updating
    const cls = await page.evaluate(() => window.__cls)
    check(`no layout shift on ${path}`, cls < 0.02, `CLS ${cls.toFixed(4)}`)
    if (cls >= 0.02) for (const sh of await page.evaluate(() => window.__shifts)) console.log(`     shift ${sh.value.toFixed(4)} at ${sh.at} ms: ${sh.nodes.join(' | ')}`)
    await page.close()
  }
} finally {
  await browser.close()
}

if (failures.length) {
  console.log(`\n${failures.length} dashboard check(s) failed: ${failures.join('; ')}`)
  process.exit(1)
}
console.log('\nall dashboard checks passed')
