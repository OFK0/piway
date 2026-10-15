import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { isPagePath, shouldIntercept } from './links.ts'

const plain = { button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, defaultPrevented: false }
const here = { origin: 'http://pi.local:8080', pathname: '/', search: '' }
const link = (href: string, extra: Partial<{ target: string; download: boolean }> = {}) => ({
  href,
  target: '',
  download: false,
  ...extra,
})

describe('shouldIntercept', () => {
  it('handles a plain click on a dashboard page', () => {
    for (const href of ['/apps', '/apps/blog', '/builds/42', '/settings', '/design-system', '/', '/?range=1m']) {
      assert.equal(shouldIntercept(plain, link(new URL(href, here.origin).href), here), true, href)
    }
  })

  it('leaves new-tab and new-window clicks to the browser', () => {
    for (const mod of ['metaKey', 'ctrlKey', 'shiftKey', 'altKey'] as const) {
      assert.equal(shouldIntercept({ ...plain, [mod]: true }, link('http://pi.local:8080/apps'), here), false, mod)
    }
    assert.equal(shouldIntercept({ ...plain, button: 1 }, link('http://pi.local:8080/apps'), here), false, 'middle click')
    assert.equal(shouldIntercept(plain, link('http://pi.local:8080/apps', { target: '_blank' }), here), false)
  })

  it('leaves files, endpoints, downloads and other sites alone', () => {
    for (const href of [
      'http://pi.local:8080/THIRD_PARTY_NOTICES.txt',
      'http://pi.local:8080/api/v1/snapshot',
      'http://pi.local:8080/metrics.json',
      'https://github.com/OFK0/piway',
      'http://pi.local:9090/apps', // another port is another origin
    ]) {
      assert.equal(shouldIntercept(plain, link(href), here), false, href)
    }
    assert.equal(shouldIntercept(plain, link('http://pi.local:8080/apps', { download: true }), here), false)
  })

  it('leaves in-page anchors and already handled clicks alone', () => {
    assert.equal(shouldIntercept(plain, link('http://pi.local:8080/#main'), here), false)
    assert.equal(shouldIntercept(plain, link('http://pi.local:8080/apps#logs'), here), true, 'anchor on another page')
    assert.equal(shouldIntercept({ ...plain, defaultPrevented: true }, link('http://pi.local:8080/apps'), here), false)
  })
})

describe('isPagePath', () => {
  it('tells pages from files and the API', () => {
    assert.equal(isPagePath('/'), true)
    assert.equal(isPagePath('/apps/my-app'), true)
    assert.equal(isPagePath('/api'), false)
    assert.equal(isPagePath('/api/v1/apps'), false)
    assert.equal(isPagePath('/favicon.svg'), false)
    assert.equal(isPagePath('/assets/index-abc.js'), false)
  })
})
