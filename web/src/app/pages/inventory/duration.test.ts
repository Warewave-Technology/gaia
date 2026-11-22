import { test } from 'node:test'
import assert from 'node:assert/strict'
import { dur } from '../../../lib/duration.ts'
import { parseDur } from './duration.ts'

test('parseDur reads what a person types', () => {
  assert.equal(parseDur('30m'), 1800)
  assert.equal(parseDur('4h'), 14400)
  assert.equal(parseDur('1d 12h'), 129600)
  assert.equal(parseDur('2 h'), 7200)
  assert.equal(parseDur('90 min'), 5400)
  assert.equal(parseDur(' 1h30m '), 5400)
})

test('parseDur refuses what is not a duration', () => {
  for (const v of ['', '4', 'h', '4 hours', '-1h', '0m', '1.5h']) assert.equal(parseDur(v), null, v)
})

test('parseDur reads back what dur() writes', () => {
  for (const s of [37, 410, 720, 13560, 61200, 187200, 604800]) assert.equal(parseDur(dur(s)), s)
})
