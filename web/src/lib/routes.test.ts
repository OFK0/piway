import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { appPath, buildPath, matchRoute } from './routes.ts'

describe('matchRoute', () => {
  it('matches every page', () => {
    assert.deepEqual(matchRoute('/'), { page: 'overview' })
    assert.deepEqual(matchRoute('/apps'), { page: 'apps' })
    assert.deepEqual(matchRoute('/apps/'), { page: 'apps' })
    assert.deepEqual(matchRoute('/apps/guestbook'), { page: 'app', name: 'guestbook' })
    assert.deepEqual(matchRoute('/builds/20261014T185759Z-b691d31'), { page: 'build', id: '20261014T185759Z-b691d31' })
    assert.deepEqual(matchRoute('/design-system'), { page: 'design-system' })
  })
  it('rejects unknown paths', () => {
    for (const p of ['/nope', '/builds', '/apps/a/b', '/design-system/x']) {
      assert.deepEqual(matchRoute(p), { page: 'not-found' }, p)
    }
  })
  it('round-trips the path helpers', () => {
    assert.deepEqual(matchRoute(appPath('my app')), { page: 'app', name: 'my app' })
    assert.deepEqual(matchRoute(buildPath('a/b')), { page: 'build', id: 'a/b' })
  })
})
