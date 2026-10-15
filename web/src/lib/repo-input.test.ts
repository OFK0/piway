import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { parseRepo } from './repo-input.ts'

describe('parseRepo', () => {
  const ok: [string, string][] = [
    ['OFK0/guestbook', 'OFK0/guestbook'],
    ['  OFK0/guestbook  ', 'OFK0/guestbook'],
    ['https://github.com/OFK0/guestbook', 'OFK0/guestbook'],
    ['https://github.com/OFK0/guestbook.git', 'OFK0/guestbook'],
    ['github.com/OFK0/guestbook/', 'OFK0/guestbook'],
    ['git@github.com:OFK0/guestbook.git', 'OFK0/guestbook'],
    ['my-org/some_repo.js', 'my-org/some_repo.js'],
  ]
  for (const [input, want] of ok) {
    it(`${input} -> ${want}`, () => assert.equal(parseRepo(input), want))
  }
  for (const input of ['', 'guestbook', 'a/b/c', 'https://gitlab.com/a/b/c', 'a b/c']) {
    it(`rejects ${JSON.stringify(input)}`, () => assert.equal(parseRepo(input), null))
  }
})
