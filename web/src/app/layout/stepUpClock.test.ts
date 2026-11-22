import { test } from 'node:test'
import assert from 'node:assert/strict'
import { STEP_UP_MARGIN_S, STEP_UP_WINDOW_S, stepUpPhase } from './stepUpClock.ts'

const s = (n: number) => n * 1000
const last = STEP_UP_WINDOW_S - STEP_UP_MARGIN_S // the last second the button still works

test('before the factor, the step-up waits for it until it is too old', () => {
  assert.deepEqual(stepUpPhase(0, null, 0), { phase: 'factor', left: last })
  assert.deepEqual(stepUpPhase(0, null, s(last - 1)), { phase: 'factor', left: 1 })
  assert.deepEqual(stepUpPhase(0, null, s(last)), { phase: 'expired', afterFactor: false })
})

test('after the factor, the button works until 120 s have passed, less the margin', () => {
  assert.deepEqual(stepUpPhase(0, s(10), s(10)), { phase: 'ready', left: last - 10 }) // the step-up was asked 10 s before
  assert.deepEqual(stepUpPhase(0, s(10), s(last - 0.5)), { phase: 'ready', left: 1 })
  assert.deepEqual(stepUpPhase(0, s(10), s(last)), { phase: 'expired', afterFactor: true })
})

test('more than 120 s between factor and button: the factor is asked again', () => {
  // a factor given at once, the button pressed 2 min 1 s later
  assert.equal(stepUpPhase(0, 0, s(121)).phase, 'expired')
  // a new step-up asked after that: its own clock starts again
  assert.deepEqual(stepUpPhase(s(130), null, s(131)), { phase: 'factor', left: last - 1 })
  assert.deepEqual(stepUpPhase(s(130), s(140), s(200)), { phase: 'ready', left: last - 70 })
})

test('the factor never buys more time than the step-up has', () => {
  // asked at 0, factor at 100 s: the step-up itself ends first
  const p = stepUpPhase(0, s(100), s(100))
  assert.deepEqual(p, { phase: 'ready', left: last - 100 })
  assert.equal(stepUpPhase(0, s(100), s(last)).phase, 'expired')
})
