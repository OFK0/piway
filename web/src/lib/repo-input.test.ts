import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { parseGitLabRepo, parseRepo } from './repo-input.ts'

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

describe('parseGitLabRepo', () => {
  const ok: [string, string, string][] = [
    ['grp/app', 'gitlab.com', 'grp/app'],
    ['grp/sub/app', '', 'grp/sub/app'],
    ['https://gitlab.com/grp/sub/app.git', 'gitlab.com', 'grp/sub/app'],
    ['https://gitlab.com/grp/app/-/tree/main', 'gitlab.com', 'grp/app'],
    ['git@gitlab.com:grp/app.git', 'gitlab.com', 'grp/app'],
    ['https://git.example.org/grp/app/', 'https://git.example.org/', 'grp/app'],
  ]
  for (const [input, host, want] of ok) {
    it(`${input} on ${host || 'default'} -> ${want}`, () => assert.equal(parseGitLabRepo(input, host), want))
  }
  for (const input of ['', 'app', 'grp//app', 'a b/c']) {
    it(`rejects ${JSON.stringify(input)}`, () => assert.equal(parseGitLabRepo(input, 'gitlab.com'), null))
  }
})
