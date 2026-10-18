// What a person does in the dashboard, against a running piway that has a
// password (PIWAY_URL, PIWAY_PASSWORD) and an app with a secret
// (PIWAY_APP, PIWAY_SECRET): the live indicator, signing in and out,
// changing a secret, and the phone width. check.mjs checks how the
// dashboard behaves; this checks that it works.
// Prints one line per check and exits 1 if any failed.
import { chromium } from 'playwright'

const base = process.env.PIWAY_URL ?? 'http://127.0.0.1:8080'
const password = process.env.PIWAY_PASSWORD
const app = process.env.PIWAY_APP ?? 'loadlab'
const secret = process.env.PIWAY_SECRET ?? 'LOADLAB_TOKEN'
if (!password) {
  console.error('PIWAY_PASSWORD is not set')
  process.exit(2)
}
const failures = []
const check = (name, ok, detail = '') => {
  console.log(`${ok ? 'ok  ' : 'FAIL'} ${name}${detail ? ': ' + detail : ''}`)
  if (!ok) failures.push(name)
}
// Runs one flow; a thrown error (a timeout waiting for something) fails it.
const flow = async (name, fn) => {
  try {
    await fn()
  } catch (e) {
    check(name, false, String(e.message ?? e).split('\n')[0])
  }
}

const browser = await chromium.launch()

const signIn = async (page, pw = password) => {
  await page.goto(base + '/')
  await page.getByLabel('Password').fill(pw)
  await page.locator('button[type=submit]').click()
}

try {
  // 1. Signing in: a wrong password is refused with a message; the right
  //    one opens the dashboard; Sign out brings the form back.
  await flow('sign-in', async () => {
    const page = await browser.newPage()
    await signIn(page, password + 'x')
    await page.getByRole('alert').first().waitFor({ timeout: 10000 })
    check('a wrong password is refused with a message', await page.getByLabel('Password').isVisible())
    await page.getByLabel('Password').fill(password)
    await page.locator('button[type=submit]').click()
    await page.getByRole('heading', { level: 1, name: 'Overview' }).waitFor({ timeout: 15000 })
    check('the right password opens the dashboard', true)
    await page.getByRole('button', { name: 'Sign out' }).click()
    await page.getByLabel('Password').waitFor({ timeout: 10000 })
    check('Sign out returns to the form', true)
    await page.close()
  })

  // Signed in, for the rest.
  const context = await browser.newContext()
  {
    const page = await context.newPage()
    await signIn(page)
    await page.getByRole('heading', { level: 1, name: 'Overview' }).waitFor({ timeout: 15000 })
    await page.close()
  }

  // 2. The live indicator: Live; closing the stream makes it Reconnecting;
  //    Retry now brings it back.
  await flow('live indicator', async () => {
    const page = await context.newPage()
    let refuse = false
    let current
    await page.routeWebSocket(/\/api\/v1\/stream/, (ws) => {
      current = ws
      if (refuse) ws.close()
      else ws.connectToServer()
    })
    await page.goto(base + '/')
    await page.getByText('Live', { exact: true }).first().waitFor({ timeout: 20000 })
    check('the overview says Live', true)
    refuse = true
    current.close()
    await page.getByText(/^Reconnecting/).first().waitFor({ timeout: 20000 })
    check('it says Reconnecting when the stream closes', true)
    refuse = false
    await page.getByRole('button', { name: 'Retry now' }).click()
    await page.getByText('Live', { exact: true }).first().waitFor({ timeout: 20000 })
    check('Retry now brings it back to Live', true)
    await page.close()
  })

  // 3. A secret: changing it asks for a restart; restarting clears that.
  await flow('secret', async () => {
    const page = await context.newPage()
    await page.goto(`${base}/apps/${app}`)
    await page.getByRole('button', { name: `Change ${secret}` }).waitFor({ timeout: 30000 })
    await page.getByRole('button', { name: `Change ${secret}` }).click()
    await page.locator('#secret-value').fill('changed-by-the-dashboard-check')
    await page.locator('[role=dialog] button[type=submit]').click()
    await page.getByText('Restart needed').waitFor({ timeout: 20000 })
    check('changing a secret shows Restart needed', true)
    await page.getByRole('button', { name: 'Restart', exact: true }).click()
    await page.getByText('Restart needed').waitFor({ state: 'hidden', timeout: 90000 })
    check('Restart clears it', true)
    await page.close()
  })

  // 4. Phone width: no page scrolls sideways (#150).
  await flow('phone width', async () => {
    const page = await context.newPage()
    await page.setViewportSize({ width: 390, height: 844 })
    for (const path of ['/', '/apps', `/apps/${app}`, '/databases', '/settings']) {
      await page.goto(base + path)
      await page.locator('h1').first().waitFor({ timeout: 20000 })
      await page.waitForFunction(() => document.querySelectorAll('[data-slot=skeleton]').length === 0, null, { timeout: 20000 })
      const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
      check(`${path} fits a phone`, over <= 0, over > 0 ? `${over}px too wide` : '')
    }
    await page.close()
  })
} finally {
  await browser.close()
}

if (failures.length > 0) {
  console.log(`\n${failures.length} dashboard flow(s) failed: ${failures.join('; ')}`)
  process.exit(1)
}
