import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ago, dur } from './duration.ts'

test('durations read the way docs/design/v3 rule 4 writes them', () => {
  assert.equal(dur(null), '—')
  assert.equal(dur(37.2), '37 s')
  assert.equal(dur(119), '119 s')
  assert.equal(dur(120), '2 min')
  assert.equal(dur(410), '6 min')
  assert.equal(dur(410, true), '6 min 50 s')
  assert.equal(dur(294, true), '4 min 54 s')
  assert.equal(dur(808, true), '13 min') // seconds only under 10 min
  assert.equal(dur(21 * 60 + 40), '21 min')
  assert.equal(dur(4 * 3600), '4 h')
  assert.equal(dur(4 * 3600 + 22 * 60 + 5), '4 h 22 min')
  assert.equal(dur(17 * 3600), '17 h')
  assert.equal(dur(4 * 86400 + 10 * 3600 + 59 * 60), '4 d 10 h')
  assert.equal(dur(2 * 86400), '2 d')
})

test('no duration looks like Go wrote it', () => {
  for (let s = 0; s < 9 * 86400; s += 997) {
    for (const p of [false, true]) assert.doesNotMatch(dur(s, p), /\d+h\d+m|\dm\d+s|\d[hms]\b/)
  }
})

test('a past duration', () => {
  assert.equal(ago(20), 'just now')
  assert.equal(ago(32 * 60), '32 min ago')
})
