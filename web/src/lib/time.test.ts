import { test } from 'node:test'
import assert from 'node:assert/strict'
import { secondsBetween, serverTime } from './time.ts'

test('times are shown in the server zone', () => {
  const utc = serverTime({ now: '2026-11-17T19:16:50Z', zone: 'Etc/UTC', offset_s: 0 })
  assert.equal(utc.clock('2026-11-17T19:10:00.746490887Z'), '19:10:00')
  assert.equal(utc.hhmm('2026-11-17T19:10:00Z'), '19:10')
  assert.equal(utc.date('2026-11-17T19:10:00Z'), 'Nov 17')
  const ist = serverTime({ now: '2026-11-17T19:16:50Z', zone: 'Europe/Istanbul', offset_s: 10800 })
  assert.equal(ist.clock('2026-11-17T19:10:00Z'), '22:10:00')
  // a zone the browser does not know: the offset decides
  const odd = serverTime({ now: '2026-11-17T19:16:50Z', zone: '+03', offset_s: 10800 })
  assert.equal(odd.clock('2026-11-17T19:10:00Z'), '22:10:00')
})

test('a missing or broken time is a dash, not an exception', () => {
  const utc = serverTime({ now: '2026-11-17T19:16:50Z', zone: 'Etc/UTC', offset_s: 0 })
  for (const bad of ['', 'not a time']) {
    assert.equal(utc.clock(bad), '—')
    assert.equal(utc.hhmm(bad), '—')
    assert.equal(utc.date(bad), '—')
  }
})

test('seconds between two times', () => {
  assert.equal(secondsBetween('2026-11-17T19:10:00Z', '2026-11-17T19:16:50Z'), 410)
})
