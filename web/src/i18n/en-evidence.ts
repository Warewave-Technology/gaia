// The evidence ledger's state in words (ADR-007 C14; docs/design/v3 rules 3 and 7), for the Evidence screen and any
// screen that shows the ledger fact. A newer server sends the state as `status` and `code` with `oldest_unchecked_at`;
// an older one sends only the latest check's verdict, time and counts, and the state is worked out from those the same
// way. Raw text (the check's problems, its reason) is never in these sentences: the screen folds it under
// "Technical details".
import { dur } from '../lib/duration.ts'

export type LedgerCode = 'ok' | 'waiting' | 'check_overdue' | 'check_failed' | 'never_checked' | 'ledger_broken' | 'evidence_missing'

/** What the screen knows of the ledger: the summary's fields, the C14 ones optional. */
export type LedgerIn = {
  checked_at: string
  verdict: string
  entries: number
  unchecked: number
  problems: string[]
  reason: string
  status?: string
  code?: string
  oldest_unchecked_at?: string | null
}

export type LedgerState = { status: 'OK' | 'WARNING' | 'CRITICAL' | 'UNKNOWN'; code: LedgerCode }

/** C14: unchecked entries younger than this are fine (the hourly check is on its way); older, a check is overdue. */
export const LEDGER_GRACE_S = 2 * 3600

const CODES: Record<LedgerCode, LedgerState['status']> = {
  ok: 'OK', waiting: 'OK', check_overdue: 'UNKNOWN', check_failed: 'UNKNOWN', never_checked: 'UNKNOWN', ledger_broken: 'CRITICAL',
  evidence_missing: 'CRITICAL',
}

/**
 * The ledger's state. `now` is the server's now (ms); `testsPassed`: whether any restore test has passed (null: not
 * known), for an older server that cannot say `evidence_missing` itself.
 */
export function ledgerState(l: LedgerIn, now: number, testsPassed: boolean | null = null): LedgerState {
  if (l.code && l.code in CODES) {
    const code = l.code as LedgerCode
    // The server's status wins when it is one of the four; never calmer than the code says.
    const st = l.status === 'OK' || l.status === 'WARNING' || l.status === 'CRITICAL' || l.status === 'UNKNOWN' ? l.status : CODES[code]
    return { status: CODES[code] !== 'OK' && st === 'OK' ? CODES[code] : st, code }
  }
  if (!l.checked_at) return { status: 'UNKNOWN', code: 'never_checked' }
  if (l.verdict === 'CRITICAL' || l.verdict === 'WARNING') return { status: 'CRITICAL', code: 'ledger_broken' }
  if (l.verdict !== 'OK') return { status: 'UNKNOWN', code: 'check_failed' }
  if (l.entries === 0 && l.unchecked === 0 && testsPassed) return { status: 'CRITICAL', code: 'evidence_missing' }
  if (l.unchecked > 0) {
    // The oldest unchecked entry is no older than the latest check when the server does not say.
    const since = l.oldest_unchecked_at || l.checked_at
    const age = (now - Date.parse(since)) / 1000
    return Number.isFinite(age) && age < LEDGER_GRACE_S ? { status: 'OK', code: 'waiting' } : { status: 'UNKNOWN', code: 'check_overdue' }
  }
  return { status: 'OK', code: 'ok' }
}

const n = (v: number) => v.toLocaleString('en-US')
const entries = (v: number) => `${n(v)} ${v === 1 ? 'entry' : 'entries'}`
const newer = (v: number) => (v === 1 ? '1 newer entry waits' : `${n(v)} newer entries wait`)

export const ledger = {
  /** The Evidence screen's answer: one sentence, no button in it. */
  answer: (s: LedgerState, l: LedgerIn, ageS: number) => ({
    ok: l.entries === 0 ? 'Nothing to prove yet: no restore test has sealed evidence.' : 'Yes. The ledger is intact and every entry is signed.',
    waiting: `Yes; ${newer(l.unchecked)} for the next hourly check.`,
    check_overdue: `Not known: the ledger has not been checked for ${dur(ageS)}.`,
    check_failed: 'Not known: the latest ledger check could not run.',
    never_checked: 'Not known yet: the ledger has never been checked.',
    ledger_broken: 'No. The ledger does not verify.',
    evidence_missing: 'No. Restore tests passed but left no evidence in the ledger.',
  })[s.code],
  /** What the answer rests on, as a sentence after it. */
  lede: (s: LedgerState, l: LedgerIn, at: string) => ({
    ok: l.entries === 0 ? `The ledger is empty (checked ${at}); the first restore test seals its first entry.`
      : `${entries(l.entries)} from the genesis entry to the head; every hash link and signature was checked ${at}.`,
    waiting: `The check ${at} verified ${entries(l.entries)}; the entries sealed since are checked within the hour.`,
    check_overdue: `The last check that ran was ${at}. ${l.unchecked > 0 ? `${newer(l.unchecked)} unchecked; ` : ''}checks run every hour, so one is overdue. Until one passes, nothing newer is vouched for.`,
    check_failed: `The check ${at} did not finish, so nothing in the ledger is vouched for until one does.`,
    never_checked: 'Until the ledger is checked, nothing here is vouched for.',
    ledger_broken: `The check ${at} found ${l.problems.length === 1 ? 'a break' : `${n(Math.max(1, l.problems.length))} problems`} in the chain or its signatures. Entries after it cannot be trusted until it is explained.`,
    evidence_missing: 'Tests that leave no evidence are a fault, not an empty ledger: the proofs they gave cannot be shown to anyone outside Gaia.',
  })[s.code],
  /** The ledger fact beside the answer: short. */
  fact: (s: LedgerState, l: LedgerIn, at: string) => ({
    ok: l.entries === 0 ? `empty, checked ${at}` : `${entries(l.entries)} verified ${at}`,
    waiting: `${entries(l.entries)} verified ${at} · ${n(l.unchecked)} wait`,
    check_overdue: `last checked ${at}`,
    check_failed: `the check ${at} did not run`,
    never_checked: 'never checked',
    ledger_broken: `does not verify (checked ${at})`,
    evidence_missing: 'tests passed, no evidence sealed',
  })[s.code],
}

/** What the screen knows of an entry's ledger record (EvidenceRow, ADR-007 C19.6); an older server sends no field. */
export type RecordIn = { kind: string; ledger_record?: string }

/** An export given while the ledger could not record it (C18.5): its record waits for the next ledger write. */
export const isNotRecorded = (r: RecordIn) => r.kind === 'export' && r.ledger_record === 'not_recorded'

/**
 * ADR-007 C19.6: an export given without a ledger record says "not recorded in the ledger", next to the download and
 * in its row, until the next successful ledger write appends its record. Nothing about evidence may look more
 * finished than it is. The ledger's reason is raw text: the screen folds it under "Technical details".
 */
export const notRecorded = {
  /** The row's result, in place of RECORDED. */
  word: 'NOT RECORDED IN THE LEDGER',
  /** Its sequence number and hash: it has neither yet. */
  none: '—',
  /** Next to the download while records wait ('' when none does). */
  waiting: (n: number) => (n <= 0 ? ''
    : n === 1 ? '1 export is not recorded in the ledger yet: its record is appended with the next successful ledger write.'
      : `${n.toLocaleString('en-US')} exports are not recorded in the ledger yet: their records are appended with the next successful ledger write.`),
  /** Next to the download, right after one the ledger could not record. */
  given: (pending: string) => (pending
    ? `Downloaded, but not recorded in the ledger: the ledger refused its record. The export says so in its README.txt and EXPORT.json; the server appends the record as ${pending} with the next successful ledger write.`
    : 'Downloaded, but not recorded in the ledger: the ledger refused its record, and the server could not keep it to append later. The audit log entry is its only record.'),
  /** The row's detail when it is opened. */
  detail: (bundle: string) =>
    `This export was given while the ledger could not record it, so it is not recorded in the ledger: no entry, no hash yet. Its record is appended as ${bundle} with the next successful ledger write (a restore test's evidence, another export, or a ledger check that finds the ledger intact).`,
  reason: 'Why the ledger refused it',
}
