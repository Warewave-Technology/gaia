import { test } from 'node:test'
import assert from 'node:assert/strict'
import { band, HI, LO, xlog } from './scale.ts'

test('the log scale starts at 30 s and ends at 7 days, clamping outside', () => {
  assert.equal(xlog(LO, 220), 0)
  assert.ok(Math.abs(xlog(HI, 220) - 220) < 1e-9)
  assert.equal(xlog(1, 220), 0)
  assert.ok(Math.abs(xlog(1e9, 220) - 220) < 1e-9)
  assert.ok(xlog(600, 220) < xlog(3600, 220))
})

test('bands against the target', () => {
  assert.equal(band(400, 1200), 'ok')
  assert.equal(band(1200, 1200), 'ok')
  assert.equal(band(2000, 1200), 'warn')
  assert.equal(band(2401, 1200), 'crit')
})
