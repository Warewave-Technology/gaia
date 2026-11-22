import { dur } from '../../../lib/duration'
import type { Messages } from '../../../i18n/en'
import type { Fmt, ServerClock } from '../../../lib/time'
import type { TestView, VMView } from '../../api/views'

export type Point = VMView['points'][number]
export type Check = { name: string; verdict: string; seconds: number; detail: string }
export type Detail = { restore_s?: number; checks?: Check[] }
export type VMStrings = Messages['vm']

/** A restore test's detail as the API records it: the restore's seconds and every check. */
export function detailOf(t: TestView): Detail {
  return (t.detail && typeof t.detail === 'object' ? t.detail : {}) as Detail
}

/** "Today 04:51" or "Nov 17 04:51" in the server's zone. */
export function when(fmt: Fmt, clock: ServerClock, iso: string, t: VMStrings): string {
  return fmt.date(iso) === fmt.date(clock.now) ? t.today(fmt.hhmm(iso)) : `${fmt.date(iso)} ${fmt.hhmm(iso)}`
}

/** A point's proof in the screen's words: the status, its word, and the sentence behind it. */
export type Proof = { status: string; word: string; text: string; note: string; test?: TestView }

/**
 * What one point is worth: damaged (its check failed), proven by a passing test, tested and not passed, being tested,
 * or backed up and not tested. A point the API has no verdict for is never shown as proven.
 */
export function proofOf(p: Point, v: VMView, t: VMStrings): Proof {
  const tests = v.tests.filter((x) => x.point_id === p.id)
  const done = tests.filter((x) => x.finished_at)
  const passed = done.find((x) => x.verdict === 'OK')
  const newest = done[0]
  const running = tests.find((x) => !x.finished_at)
  const ret = v.status.proven?.point_id === p.id
  if (p.check_state === 'failed') return { status: 'CRITICAL', word: t.word.damaged, text: t.pt.damaged(p.reason), note: t.pnote.damaged }
  if (passed) {
    const rto = passed.proven_rto_s !== null ? dur(passed.proven_rto_s, true) : null
    return {
      status: 'OK', word: t.word.proven, test: passed,
      text: t.pt.proven(passed.id, rto) + (ret ? ` ${t.pt.returnHere}` : ''),
      note: t.pnote.test(passed.id, rto),
    }
  }
  if (newest) {
    const st = newest.verdict === 'CRITICAL' || newest.verdict === 'WARNING' ? newest.verdict : 'UNKNOWN'
    const word = st === 'CRITICAL' ? t.word.failed : st === 'WARNING' ? t.word.warned : t.word.notProven
    const text = st === 'CRITICAL' ? t.pt.failed(newest.id, newest.reason) : st === 'WARNING' ? t.pt.warned(newest.id, newest.reason) : t.pt.undecided(newest.id, newest.reason)
    return { status: st, word, text, note: t.pnote.testReason(newest.id, newest.reason), test: newest }
  }
  if (running) return { status: 'UNKNOWN', word: t.word.testing, text: t.pt.testing(running.id), note: t.pnote.testing(running.id), test: running }
  if (p.check_state === 'ok') return { status: 'OK', word: t.word.backedUp, text: t.pt.backedUp, note: t.pnote.notTested }
  return { status: 'UNKNOWN', word: t.word.notChecked, text: t.pt.notChecked, note: t.pnote.notChecked }
}
