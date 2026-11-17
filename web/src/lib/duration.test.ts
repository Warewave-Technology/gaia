import { test } from 'node:test'
import assert from 'node:assert/strict'
import { dur } from './duration.ts'

test('durations read the way the mock-up shows them', () => {
  assert.equal(dur(null), '—')
  assert.equal(dur(37.2), '37s')
  assert.equal(dur(410), '6m 50s')
  assert.equal(dur(720), '12m')
  assert.equal(dur(808), '13m')
  assert.equal(dur(808, true), '13m 28s')
  assert.equal(dur(3 * 3600 + 46 * 60 + 5), '3h 46m')
  assert.equal(dur(17 * 3600), '17h')
  assert.equal(dur(2 * 86400 + 4 * 3600), '2d 4h')
})
