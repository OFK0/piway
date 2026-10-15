import assert from 'node:assert/strict'
import { describe, it } from 'node:test'

import { buildCause, doneVerb, runningVerb } from './build-labels.ts'

describe('build labels', () => {
  it('names every kind of build', () => {
    assert.equal(buildCause({ pusher: 'octocat' }), 'Push')
    assert.equal(buildCause({ pusher: 'api' }), 'Deploy')
    assert.equal(buildCause({ pusher: 'dashboard' }), 'Redeploy')
    assert.equal(buildCause({ kind: 'rollback', pusher: 'dashboard' }), 'Rollback')
    assert.equal(buildCause({ kind: 'restart', pusher: 'api' }), 'Restart')
    assert.equal(doneVerb({ kind: 'restart' }), 'restarted')
    assert.equal(doneVerb({}), 'deployed')
    assert.equal(runningVerb({ kind: 'rollback' }), 'Rolling back')
  })
})
