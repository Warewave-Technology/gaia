// The VM page's words and marks, without React, so node --test checks them (logic.test.ts): a point's proof, a test's
// steps as sentences, a status change's why, and the history strip's marks merged when they are closer than 6 px.
import { checkName, reason } from '../../../i18n/en-reasons.ts'
import type { vm3 } from '../../../i18n/en-vm.ts'
import { dur } from '../../../lib/duration.ts'
import type { PointTest, VMPoint, VMStatusChange } from '../../api/vm'
import type { TestView, TimelineEvent } from '../../api/views'
import type { Proof } from '../../ui/PointProof'

export type T = typeof vm3

// --- the history strip's marks (docs/design/v3 rule 6) ---

/** Marks closer than this merge into one with a count. */
export const MERGE_PX = 6
export type Lane = 'test' | 'backup'
/** A mark's look: a test passed / warned / failed / could not run; a backup kept / damaged / failed. */
export type Look = 'p' | 'w' | 'f' | 'u' | 'b' | 'd' | 'x'
export type Mark = { e: TimelineEvent; key: string; lane: Lane; look: Look; px: number }
export type Group = { lane: Lane; px: number; marks: Mark[]; look: Look }

export const LOOK: Record<string, [Lane, Look]> = {
  test_ok: ['test', 'p'], test_warning: ['test', 'w'], test_failed: ['test', 'f'], test_unknown: ['test', 'u'],
  backup: ['backup', 'b'], damaged: ['backup', 'd'], backup_failed: ['backup', 'x'],
}
// The worst look of a merged mark wins.
const RANK: Record<Look, number> = { f: 0, x: 0, d: 1, u: 2, w: 3, p: 4, b: 4 }
export const keyOf = (e: TimelineEvent) => `${e.kind}:${e.id}`

/** The strip's marks at `width` px for the window [start, start + span] (ms); events outside it or of no lane are left out. */
export function marksOf(events: readonly TimelineEvent[], start: number, span: number, width: number): Mark[] {
  return events.flatMap((e) => {
    const lk = LOOK[e.kind]
    const f = (Date.parse(e.at) - start) / span
    if (!lk || !(f >= 0 && f <= 1)) return []
    return [{ e, key: keyOf(e), lane: lk[0], look: lk[1], px: f * width }]
  })
}

/** Marks closer than MERGE_PX to their neighbour on one lane become one group, drawn with the worst look and a count. */
export function groups(marks: readonly Mark[]): Group[] {
  const out: Group[] = []
  for (const lane of ['test', 'backup'] as const) {
    const ms = marks.filter((m) => m.lane === lane).sort((a, b) => a.px - b.px)
    let g: Mark[] = []
    const flush = () => {
      if (!g.length) return
      const look = g.reduce((w, m) => (RANK[m.look] < RANK[w] ? m.look : w), g[0].look)
      out.push({ lane, marks: g, look, px: g.reduce((a, m) => a + m.px, 0) / g.length })
      g = []
    }
    for (const m of ms) {
      if (g.length && m.px - g[g.length - 1].px >= MERGE_PX) flush()
      g.push(m)
    }
    flush()
  }
  return out
}

// --- a point's proof (ADR-007 C16) ---

/** Why a test did not pass, in a few words. */
function failedWhy(x: PointTest, t: T) {
  return x.failed_check === 'boot' ? t.pdetail.noBoot : x.failed_check === 'stable' ? t.pdetail.rebooted
    : x.failed_check ? t.pdetail.check(checkName(x.failed_check)) : ''
}

/**
 * What one point is worth: DAMAGED when its check found it so, PROVEN only when a restore test of it passed, FAILED
 * when its newest test failed, UNKNOWN when its test could not run (it should have been tested and could not be), and
 * BACKED UP, "not tested", otherwise.
 */
export function proofOf(r: VMPoint, ret: number | null, t: T): { proof: Proof; note: string; detail: string } {
  if (r.check_state === 'failed') return { proof: 'DAMAGED', note: t.pnote.damaged, detail: t.pdetail.damaged }
  if (r.proven) {
    const rto = r.proven.proven_rto_s !== null ? dur(r.proven.proven_rto_s, true) : null
    return { proof: 'PROVEN', note: r.id === ret ? t.pnote.returnHere : '', detail: t.pdetail.proven(r.proven.id, rto) }
  }
  if (r.running_test_id) return { proof: 'BACKED_UP', note: t.pnote.testing, detail: t.pdetail.testing(r.running_test_id) }
  const l = r.latest
  if (l?.verdict === 'CRITICAL') return { proof: 'FAILED', note: '', detail: t.pdetail.failed(l.id, failedWhy(l, t) || t.pdetail.failedAny) }
  if (l?.verdict === 'WARNING') return { proof: 'BACKED_UP', note: t.pnote.warned, detail: t.pdetail.warned(l.id) }
  if (l) return { proof: 'UNKNOWN', note: t.pnote.couldNotRun, detail: t.pdetail.notRun(l.id) }
  if (r.check_state !== 'ok') return { proof: 'BACKED_UP', note: t.pnote.checkPending, detail: t.pdetail.checkPending }
  // Only a full backup's reason says something ("a disk was resized"); an incremental one is the usual case.
  return { proof: 'BACKED_UP', note: t.pnote.notTested, detail: r.kind === 'full' ? r.kind_reason : '' }
}

// --- a restore test in words (docs/design/v3 rule 3) ---

export type Check = { name: string; type?: string; verdict: string; seconds?: number; detail?: string }
export type Detail = { restore_s?: number; checks?: Check[]; errors?: string[]; sandbox_down?: boolean }

/** A restore test's detail as the API records it: the restore's seconds, every check, its errors. */
export function detailOf(t: Pick<TestView, 'detail'>): Detail {
  return (t.detail && typeof t.detail === 'object' ? t.detail : {}) as Detail
}

/** What a finished test says about its point, in one sentence (the strip's caption). */
export function testSays(test: Pick<TestView, 'detail' | 'verdict' | 'proven_rto_s'>, t: T): string {
  const d = detailOf(test)
  const failed = d.checks?.find((c) => c.verdict === 'CRITICAL')
  switch (test.verdict) {
    case 'OK': return test.proven_rto_s !== null ? t.testSays.passedRto(dur(test.proven_rto_s, true)) : t.testSays.passed
    case 'WARNING': return t.testSays.warned
    case 'CRITICAL':
      if (!failed) return t.testSays.failed
      if (failed.name === 'boot') return t.testSays.noBoot
      if (failed.name === 'stable') return t.testSays.rebooted
      return t.testSays.check(checkName(failed.name))
    default: return d.sandbox_down ? t.testSays.sandboxDown : t.testSays.notRun
  }
}

/** "within 300 s" in a check's detail, as a duration ("5 min"). */
function within(detail: string | undefined): string | null {
  const m = detail?.match(/within (\d+) ?s\b/)
  return m ? dur(Number(m[1])) : null
}

/** One step of a test: its name and the sentence an operator would say; `notRun` for a step skipped. */
export function stepOf(c: Check, t: T): { name: string; text: string; notRun: boolean } {
  const ok = c.verdict === 'OK'
  const bad = c.verdict === 'CRITICAL'
  const notRun = !ok && !bad && /^not run/i.test(c.detail ?? '')
  const type = c.type ?? ''
  const typeName = t.checkTypes[type] ?? type.toUpperCase()
  let name = t.cap(checkName(c.name))
  let text: string
  if (c.name === 'boot' || type === 'boot_serial') {
    // Only say "serial console" when the check says so: a test without its check's type or detail just booted or not.
    const serial = type === 'boot_serial' || /serial console/.test(c.detail ?? '')
    if (ok) text = type === 'winrm' ? t.step.bootOk.winrm : serial ? t.step.bootOk.serial : type ? t.step.bootOk.net(typeName) : t.step.bootOk.any
    else if (/stopped before boot/.test(c.detail ?? '')) text = t.step.bootNo.stopped
    else if (serial) text = t.step.bootNo.serial(within(c.detail))
    else text = type ? t.step.bootNo.net(typeName, within(c.detail)) : t.step.bootNo.any
    name = t.cap(checkName('boot'))
  } else if (c.name === 'stable') {
    text = ok ? t.step.stableOk : t.step.stableNo
  } else if (c.name.startsWith('image_unchanged') || type === 'image_hash') {
    const disk = c.name.replace(/^image_unchanged_?/, '')
    name = disk ? t.step.unchangedDisk(disk) : t.step.unchanged
    text = ok ? t.step.imageOk : t.step.imageNo
  } else {
    text = ok ? t.step.probeOk(typeName || c.name) : bad ? t.step.probeNo(typeName || c.name) : t.step.unknown
  }
  if (notRun) text = /did not boot/.test(c.detail ?? '') ? t.step.skipped : t.step.notRun
  return { name, text, notRun }
}

// --- a status change's why ---

/** Why the status changed, as a sentence from the change's code; the words the server recorded are details. */
export function whyOf(c: Pick<VMStatusChange, 'code'>, t: T): string {
  if (!c.code) return t.whyNoCode
  return t.why[c.code] ?? t.whyOther(reason(c.code).title)
}
