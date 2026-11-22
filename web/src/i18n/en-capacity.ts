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
  sandboxDown: (err: string) =>
    `The sandbox helper did not answer${err ? ` (${err})` : ''}. No restore test can run, so nothing new is proven; the numbers below count no slots until it answers.`,
  facts: 'The sandbox',
  slotsNow: 'Test slots',
  slotsText: (slots: number, basis: string) => `${n(slots)}${basis ? `, ${basis}` : ''}`,
  memory: 'Memory for tests',
  memoryText: (used: number, limit: number) => `${n(used)} MiB in use of ${n(limit)} MiB`,
  runningNow: 'Running now',
  running: (r: number) => (r === 0 ? 'No restore test is running.' : `${plural(r, 'restore test', 'restore tests')}`),
  waitingMem: 'Waiting for memory',
  waiting: (w: number) => (w === 0 ? 'none' : plural(w, 'test', 'tests')),
  noAnswer: 'does not answer',

  model: 'Model a change',
  modelHint: 'A model only: nothing here is saved or changes a schedule. Slots and policies are changed in Inventory.',
  slots: 'Parallel test slots',
  interval: 'Test interval',
  policy: 'Policy',
  asSet: (d: string) => `as set · ${d}`,
  modelled: (what: string) => `Modelled: ${what}`,
  slotsChange: (from: number, to: number) => `${plural(to, 'slot', 'slots')} (now ${n(from)})`,
  policyChange: (name: string, to: string, from: string) => `${name} every ${to} (now ${from})`,
  reset: 'Back to now',
  modelling: 'Modelling…',
  col: { what: 'Figure', now: 'Now', model: 'Modelled' },
  row: {
    slots: 'Slots', capacity: 'Capacity', demand: 'Demand', coverage: 'Covers', needed: 'Slots needed',
    expiring: (h: string) => `Proofs that expire in the next ${h}`, tests: 'Tests a day',
  },
  slotHours: (h: number) => `${n(Math.round(h))} slot-hours a day`,
  pct: (p: number) => `${p} %`,
  barNow: 'Now',
  barModel: 'Modelled',
  barLabel: (need: number, cap: number) => `Demand ${n(Math.round(need))} slot-hours a day, capacity ${n(Math.round(cap))}`,
  classLegend: (name: string, h: number, tests: number, median: string) =>
    `${name} tests ${n(Math.round(h))} slot-hours a day (${n(Math.round(tests))} tests, ${median} each)`,
  capLegend: (h: number) => `capacity ${n(Math.round(h))} slot-hours a day`,
  gap: 'not covered',
  osClass: (c: string) => ({ linux: 'Linux', windows: 'Windows' } as Record<string, string>)[c] ?? c,
  note: 'One slot runs one restore test at a time. A test is expected to take the median of its VM\'s last 10 tests, else the median of its OS class, else a default.',

  policies: 'Demand by policy',
  pcol: { policy: 'Policy', vms: 'VMs', every: 'Test every', tests: 'Tests a day', each: 'Median test', hours: 'Slot-hours a day', model: 'Modelled' },
  share: (p: number) => `${p} % of test time`,
  noPolicies: 'No VM has a policy yet.',

  expiring: (h: string) => `Proofs that expire in the next ${h}`,
  expiringHint: 'Before their next test finishes, with the queue order and slots of now.',
  ecol: { vm: 'VM', policy: 'Policy', expires: 'Proof expires', next: 'Next test' },
  nextTest: (start: string, fin: string) => `${start} to ${fin}`,
  noTest: (h: string) => `none in the next ${h}`,
  noneExpiring: 'Every proof is re-tested before it expires.',
  expiringUnknown: 'Not known while the sandbox does not answer.',

  queue: 'Test queue',
  queueHint: (w: number) => `${n(w)} waiting · in order: retests of CRITICAL VMs first, then never proven, then nearest to expiry`,
  qcol: { pos: '#', vm: 'VM', why: 'Why this place', expected: 'Expected', starts: 'Starts' },
  run: 'run',
  wait: 'wait',
  slot: (s: number, slots: number) => `slot ${s} of ${slots}`,
  runningFor: (d: string) => `running, ${d}`,
  waitingFor: (why: string) => `waiting for memory${why ? `: ${why}` : ''}`,
  memMiB: (m: number) => `${n(m)} MiB`,
  job: (id: number) => `job ${id}`,
  notInHorizon: (h: string) => `not in the next ${h}`,
  queueEmpty: 'No restore test is waiting.',
  today: (hhmm: string) => `Today ${hhmm}`,
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
