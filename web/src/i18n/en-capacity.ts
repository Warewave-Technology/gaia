// The Proof capacity and Activity screens' strings (T27; docs/design/v2/Capacity.dc.html, Activity.dc.html).
const n = (v: number) => v.toLocaleString('en-US')
const plural = (v: number, one: string, many: string) => `${n(v)} ${v === 1 ? one : many}`

/** A size in the unit that keeps it short: 820 MiB, 1.4 GiB, 25.5 TiB. */
function size(bytes: number) {
  const units = ['bytes', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 || v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

const KINDS: Record<string, [string, string]> = {
  backup: ['backup', 'backups'], restore_test: ['restore test', 'restore tests'], verify_repository: ['repository verification', 'repository verifications'],
  state_backup: ['state backup', 'state backups'], migration_check: ['migration check', 'migration checks'],
}

const cls = (c: string) => ({ linux: 'Linux', windows: 'Windows' } as Record<string, string>)[c] ?? c

/** Why a queued test is where it is (internal/capacity reasonOf), as a sentence; `at` writes a time the screen's way. */
export function orderReason(reason: string, at: (iso: string) => string): string {
  if (reason.startsWith('retest of a CRITICAL VM')) return 'Retest of a CRITICAL VM: these go first'
  if (reason === 'never proven') return 'Never proven: its first restore test'
  const m = reason.match(/^proof expires (\S+)(?: \(policy (.+)\))?$/)
  if (m) return `Its proof expires ${at(m[1])}`
  return reason
}

/** Where a VM's expected test duration comes from (internal/capacity Expected), as words. */
export function durationBasis(basis: string): string {
  let m = basis.match(/^median of its last (\d+) tests?$/)
  if (m) return m[1] === '1' ? 'its last test' : `median of its last ${m[1]} tests`
  m = basis.match(/^median of the (\S+) class$/)
  if (m) return `no test of its own yet: median of ${cls(m[1])} VMs`
  m = basis.match(/^default for (\S+)$/)
  if (m) return `no test measured yet: the default for ${cls(m[1])}`
  return basis
}

/** Where a VM's OS class comes from (ADR-007 C13: the `os:` tag, then libvirt's guest information, then the check
 * template's name). Unknown words are shown as they are. */
export function osClassBasis(basis: string | undefined): string {
  if (!basis) return ''
  const b = basis.toLowerCase()
  if (b.includes('tag')) return 'from its os: tag'
  if (b.includes('libvirt') || b.includes('guest')) return 'from libvirt\'s guest information'
  if (b.includes('template')) return 'guessed from its check template\'s name'
  return basis
}

export const capacity = {
  title: 'Proof capacity',
  question: 'Can the sandbox keep every proof fresh?',
  yesSpare: (pct: number) => `Yes, with ${pct} % to spare.`,
  yesNoDemand: 'Yes. No policy asks for a restore test yet.',
  no: (pct: number) => `No. It covers ${pct} % of what policies ask.`,
  unknown: 'Not known: the sandbox does not answer.',
  ask: (tests: number, vms: number) => `Policies ask for ${plural(tests, 'restore test', 'restore tests')} a day across ${plural(vms, 'VM', 'VMs')}.`,
  ok: (slots: number) => `${plural(slots, 'slot runs', 'slots run')} them all.`,
  behind: (slots: number, expiring: number, horizon: string) =>
    `With ${plural(slots, 'slot', 'slots')}, ${expiring > 0 ? `${plural(expiring, 'proof expires', 'proofs expire')} before the next test in the next ${horizon}` : 'the tests fall behind'}.`,
  need: (needed: number) => `You need ${plural(needed, 'slot', 'slots')}, or longer test intervals.`,
  sandboxDown: 'No restore test runs and nothing new is proven until the sandbox helper answers.',
  /** While the sandbox does not answer the server models from the slot count it last reported (ADR-007 C17). */
  modelFromLastKnown: (slots: number, at: string, pct: number) =>
    `The numbers below are modelled from the ${plural(slots, 'slot', 'slots')} it last reported, at ${at}: they would cover ${pct} % of what policies ask.`,
  neverReported: 'It has never reported its slots, so the numbers below count none.',
  lastKnown: (slots: number, at: string) => `last known: ${plural(slots, 'slot', 'slots')}, at ${at}`,
  facts: 'The sandbox',
  slotsNow: 'Test slots',
  slotsText: (slots: number, basis: string) => `${n(slots)}${basis ? `, ${basis}` : ''}`,
  memory: 'Memory for tests',
  memoryText: (used: number, limit: number) => `${n(used)} MiB in use of ${n(limit)} MiB`,
  runningNow: 'Running now',
  running: (r: number) => (r === 0 ? 'no restore test' : plural(r, 'restore test', 'restore tests')),
  waitingMem: 'Waiting for memory',
  waiting: (w: number) => (w === 0 ? 'none' : plural(w, 'test', 'tests')),
  noAnswer: 'does not answer',

  model: 'Model a change',
  modelHint: 'Nothing here is saved or changes a schedule. Slots are set in the sandbox unit, intervals in Inventory.',
  slots: 'Parallel test slots',
  interval: 'Test interval',
  policy: 'Policy',
  asSet: (d: string) => `as set · ${d}`,
  modelled: (what: string) => `Modelled: ${what}`,
  slotsChange: (from: number, to: number) => `${plural(to, 'slot', 'slots')} (now ${n(from)})`,
  policyChange: (name: string, to: string, from: string) => `${name} every ${to} (now ${from})`,
  reset: 'Back to now',
  modelling: 'Modelling…',
  fig: { slots: 'Slots', covers: 'Covers', needed: 'Slots needed', tests: 'Tests a day', expiring: (h: string) => `Proofs expiring, ${h}` },
  arrow: (now: string, model: string) => `${now} → ${model}`,
  pct: (p: number) => `${p} %`,
  barNow: 'Now',
  barModel: 'Modelled',
  barLabel: (need: number, cap: number) => `Demand ${n(Math.round(need))} slot-hours a day, capacity ${n(Math.round(cap))}`,
  segment: (name: string, h: number) => `${name} ${n(Math.round(h))} slot-h`,
  classLegend: (name: string, h: number) => `${name} tests, ${n(Math.round(h))} slot-hours a day`,
  otherClasses: 'other OS classes',
  capLegend: (h: number) => `capacity, ${n(Math.round(h))} slot-hours a day`,
  gap: 'not covered',
  osClass: cls,
  note: 'One slot runs one restore test at a time. A test is expected to take the median of its VM\'s last 10 tests, else the median of its OS class, else a default (Linux 3 min, Windows 10 min).',

  tabs: 'Proof capacity in detail',
  tab: { demand: 'Demand', expiring: 'Proofs that expire', queue: 'Test queue', inputs: 'Expected durations' },
  hint: {
    demand: 'What each OS class and policy asks of the sandbox.',
    expiring: (h: string) => `Proofs that expire before their next test finishes, in the next ${h}, with the queue and slots of now.`,
    queue: 'In order: retests of CRITICAL VMs first, then never proven, then nearest to expiry.',
    inputs: 'What each VM\'s next test is expected to take, and why.',
  },

  classes: 'Demand by OS class',
  policies: 'Demand by policy',
  col: {
    cls: 'OS class', policy: 'Policy', vms: 'VMs', every: 'Test every', tests: 'Tests a day', each: 'Median test', hours: 'Slot-hours a day',
    share: 'Share', model: 'Modelled',
  },
  share: (p: number) => `${p} %`,
  noPolicies: 'No VM has a policy yet.',

  ecol: { vm: 'VM', policy: 'Policy', expires: 'Proof expires', next: 'Next test' },
  expired: (at: string) => `expired ${at}`,
  nextTest: (start: string, fin: string) => `${start} to ${fin}`,
  noTest: (h: string) => `none in the next ${h}`,
  noneExpiring: 'Every proof is re-tested before it expires.',
  expiringUnknown: 'Not known while the sandbox does not answer.',
  expiringCut: (shown: number, total: number) => `The ${n(shown)} that expire first are listed, of ${n(total)}.`,

  queueCaption: 'Test queue',
  nowCaption: 'Running now and waiting for memory',
  qcol: { pos: '#', vm: 'VM', state: 'State', why: 'Why this place', expected: 'Expected', where: 'Slot', note: 'Why it waits' },
  slot: (s: number, slots: number) => `slot ${s} of ${slots}`,
  runningFor: (d: string) => `for ${d}`,
  waitingSince: (at: string) => `since ${at}`,
  waitingMemory: 'waiting for memory',
  memMiB: (m: number) => `${n(m)} MiB`,
  position: (p: number) => `#${n(p)}`,
  startsAt: (at: string) => `starts ${at}`,
  notInHorizon: (h: string) => `not in the next ${h}`,
  queueEmpty: 'No restore test is waiting.',
  queueUnknown: 'No restore test starts while the sandbox does not answer.',
  basisOf: (b: string) => `Expected: ${b}`,

  icol: { vm: 'VM', policy: 'Policy', cls: 'OS class', every: 'Test every', age: 'Max proof age', expected: 'Expected', basis: 'Why' },
  durations: (ds: string) => `Last tests, newest first: ${ds}`,
  noDurations: 'No restore test of this VM has finished yet.',
  classMedians: (parts: string) => `Medians by OS class: ${parts}`,
  noInputs: 'No VM has a policy that tests it.',
}

export const jobs = {
  title: 'Activity',
  question: 'What is Gaia doing?',
  idle: 'Nothing is running or waiting.',
  answer: (running: number, queued: number) => `${n(running)} running, ${n(queued)} queued`,
  running: 'Running',
  queued: 'Queued',
  lastHour: 'Last hour',
  finished: (failed: number) => `finished · ${n(failed)} failed`,
  nothing: 'nothing',
  kindCount: (k: string, c: number) => plural(c, KINDS[k]?.[0] ?? k, KINDS[k]?.[1] ?? k),
  totals: 'Jobs now and in the last hour',
  f: {
    kind: 'Kind', state: 'State', host: 'Host', any: 'any', clearAll: 'Clear filters',
    kinds: [['', 'All'], ['backup', 'Backups'], ['restore_test', 'Restore tests'], ['verify_repository', 'Verifications'], ['state_backup', 'State backups'],
      ['migration_check', 'Migration checks']] as const,
    states: [['', 'Any state'], ['running', 'Running'], ['queued', 'Queued'], ['failed', 'Failed'], ['ok', 'Done'], ['cancelled', 'Cancelled']] as const,
  },
  updates: (s: number) => `Updates every ${s} s · newest first`,
  col: { state: 'State', job: 'Job', target: 'Target', result: 'Result', started: 'Started', took: 'Took', by: 'By' },
  word: { running: 'RUNNING', queued: 'QUEUED', ok: 'DONE', failed: 'FAILED', cancelled: 'CANCELLED' } as Record<string, string>,
  doneWith: (verdict: string) => `DONE, ${verdict}`,
  kind: (k: string) => ({
    backup: 'Backup', restore_test: 'Restore test', verify_repository: 'Repository verification', state_backup: 'State backup', migration_check: 'Migration check',
  } as Record<string, string>)[k] ?? k,
  jobId: (id: number) => `job ${id}`,
  testId: (id: number) => `test ${id}`,
  checkId: (id: number) => `check ${id}`,
  noVM: 'no VM',
  point: (id: number, bytes: number | null) => `point ${id}${bytes !== null ? ` · ${size(bytes)}` : ''}`,
  ready: (d: string) => `ready in ${d}`,
  waitingSince: (at: string) => `queued ${at}`,
  runningNow: 'running',
  today: (t: string) => t,
  empty: 'No job has run yet.',
  noneMatch: 'No job matches these filters.',
}
