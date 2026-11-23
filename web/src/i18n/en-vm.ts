// The VM page's strings, v3 (T27 step 1b; docs/design/v3/VM3.dc.html). Times and durations come in already written
// (lib/time `fmt.at`, lib/duration `dur`); sentences about a reason are built from its code (en-reasons.ts).
const n = (v: number) => v.toLocaleString('en-US')
const plural = (v: number, one: string, many: string) => `${n(v)} ${v === 1 ? one : many}`
const cap = (s: string) => (s ? s[0].toUpperCase() + s.slice(1) : s)

/** A size in the unit that keeps it short: 820 MiB, 1.4 GiB. */
function size(bytes: number) {
  const units = ['bytes', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 || v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`
}

export const vm3 = {
  cap,
  size,
  /** A moment as the answer band and the flag say it: `fmt.at` with "today" before a bare time. */
  day: (at: string) => (/^\d\d:\d\d$/.test(at) ? `today ${at}` : at),
  since: (at: string, d: string) => `since ${at} (${d})`,

  // header
  backupNow: 'Back up now',
  testNow: 'Test newest point',
  more: 'More',
  moreLabel: 'More actions',
  editTags: 'Edit tags…',
  changePolicy: 'Change policy…',
  queued: (what: string, id: number) => `${what}: queued as job ${id}.`,
  meta: {
    label: 'Where this VM is protected',
    targets: (rpo: string, rto: string, test: string) => `(RPO ${rpo}, RTO ${rto}, test every ${test})`,
    noPolicy: 'no policy',
    repository: 'repository',
    noTags: 'no tags',
  },
  tags: { label: 'Tags', hint: 'key or key:value, separated by spaces', save: 'Save tags', cancel: 'Cancel' },
  policy: {
    label: 'Policy',
    none: 'No policy (nothing is backed up)',
    save: 'Assign policy',
    cancel: 'Cancel',
    stepUp: 'Change the VM’s policy',
    weaker: 'A policy with looser targets asks for your second factor again.',
  },

  // answer band
  answer: 'What this VM can return to',
  returnTo: 'If it breaks now, you return to',
  pointN: (id: number) => `point ${id}`,
  provenBy: (at: string, rto: string | null) =>
    `Proven by a restore test at ${at}.${rto ? ` It would be running again in about ${rto}.` : ' The test measured no restore time.'}`,
  nothing: 'Nothing proven yet',
  nothingWhat: 'No restore test of a point that is still kept has passed. Until one does, there is no point Gaia can vouch for.',
  testsFailedSince: (k: number) => (k === 1 ? 'The restore test since then failed.' : `The ${n(k)} restore tests since then failed.`),
  also: 'Also:',
  note: 'Note:',
  sameCause: (k: number, where: string) => `Same cause on ${plural(k, 'other VM', 'other VMs')}${where}.`,
  onHosts: (hosts: string[], more: boolean) => (hosts.length ? ` on ${hosts.join(', ')}${more ? ' and more' : ''}` : ''),
  inScope: (kind: string, name: string) => ` (${kind} ${name})`,
  onlyThis: 'No other VM has this cause.',
  seeAll: (k: number) => `See all ${n(k)}`,
  reasonDetails: 'Technical details of this reason',

  // the four measures
  measures: 'Measures against the targets',
  provenRpo: 'Proven RPO',
  newestBackup: 'Newest backup',
  ago: ' ago',
  provenRto: 'Proven RTO',
  proofAge: 'Proof age',
  target: (d: string) => `target ${d}`,
  max: (d: string) => `max ${d}`,
  noTarget: 'no target: no policy',
  noValue: { rpo: 'nothing proven', backup: 'no backup yet', rto: 'not measured', age: 'nothing proven' },

  // history strip
  last: (h: number) => (h === 168 ? 'Last 7 days' : 'Last 24 hours'),
  range: 'Range',
  h24: '24 h',
  d7: '7 days',
  strip: (h: number) => `Backups and restore tests, ${h === 168 ? 'last 7 days' : 'last 24 hours'}`,
  laneTests: 'Restore tests',
  laneBackups: 'Backups',
  returnHere: (at: string) => `▼ return here · ${at}`,
  returnHereEnd: (at: string) => `return here · ${at} ▼`,
  returnBefore: (at: string) => `◀ return here · ${at}, before this window`,
  nothingProven: '◀ nothing proven in this window or before',
  rpoTarget: 'RPO target',
  now: 'now',
  legend: { passed: 'test passed', failed: 'test failed', notRun: 'test could not run', lose: 'not proven: what you would lose' },
  mark: {
    backup: (id: number, at: string) => `Backup at ${at}, point ${id}`,
    damaged: (id: number, at: string) => `Damaged point ${id}, backed up at ${at}`,
    backupFailed: (job: number, at: string) => `Backup failed at ${at}, job ${job}`,
    test: (word: string, id: number, at: string) => `Restore test ${id} at ${at}: ${word}`,
    group: (k: number, what: string, from: string, to: string) => `${n(k)} ${what} from ${from} to ${to}`,
    backups: 'backups',
    tests: 'restore tests',
  },
  sel: {
    returnTitle: (at: string) => `Point at ${at}`,
    returnText: 'The newest point a restore test proved. Everything after it is backed up but not proven.',
    returnNewest: 'The newest point a restore test proved. Nothing newer exists yet.',
    nothingTitle: 'No proven point',
    nothingText: 'No restore test of a kept point has passed: everything here is backed up at best, not proven.',
    testTitle: (at: string) => `Restore test at ${at}`,
    backupTitle: (at: string) => `Backup at ${at}`,
    failedTitle: (at: string) => `Backup at ${at}`,
    groupTitle: (k: number, what: string, from: string, to: string) => `${n(k)} ${what}, ${from} to ${to}`,
    proven: 'This is the point you would return to.',
    newer: 'Backed up and checked, not proven by a restore test.',
    older: 'Older than the point you would return to.',
    tested: (detail: string) => `Backed up, not proven: ${detail}.`,
    damaged: 'Its check found it damaged: not a point you can return to.',
    backupFailed: (job: number) => `Backup job ${job} failed: no point was made.`,
    unchecked: 'Backed up; its check has not run yet.',
    showPoint: (id: number) => `Show point ${id} in Points`,
    groupText: (parts: string) => `${parts}. Choose a narrower range to see each one.`,
  },
  word: {
    returnHere: 'RETURN HERE', passed: 'PASSED', failed: 'FAILED', warned: 'WARNING', notRun: 'NOT RUN', running: 'RUNNING',
    backedUp: 'BACKED UP', proven: 'PROVEN', damaged: 'DAMAGED', backupFailed: 'FAILED', ok: 'OK', unknown: 'UNKNOWN',
  },
  testSays: {
    passed: 'Restored, booted and every check passed.',
    passedRto: (rto: string) => `Restored, booted and every check passed; running again in ${rto}.`,
    warned: 'Restored and booted; a check warned, so the point is not proven.',
    noBoot: 'Restored, but the copy did not boot.',
    rebooted: 'Restored and booted, but the copy restarted by itself.',
    check: (check: string) => `Restored and booted, but the check “${check}” failed.`,
    failed: 'Restored, but a check failed.',
    sandboxDown: 'The test could not run: the sandbox did not answer. It proves nothing.',
    notRun: 'The test could not decide. It proves nothing.',
  },

  // tabs
  tabs: 'About this VM',
  tab: { test: 'Latest test', points: 'Points', history: 'Status history', evidence: 'Evidence' },

  // latest test
  noTests: 'No restore test has finished yet.',
  testOf: (test: number, point: number, at: string) => `test ${test} · point ${point} · ${at}`,
  step: {
    restore: 'Restore',
    restoreText: (point: number) => `Copied point ${point} into the sandbox`,
    notRunWord: 'NOT RUN',
    skipped: 'Skipped: the copy did not boot.',
    notRun: 'Not run.',
    unchanged: 'Unchanged',
    unchangedDisk: (disk: string) => `Unchanged (${disk})`,
    bootOk: { serial: 'A login prompt appeared on the serial console', winrm: 'Windows answered on WinRM', net: (type: string) => `The copy answered on ${type} as it booted`, any: 'The copy booted' },
    bootNo: {
      serial: (within: string | null) => `No login prompt on the serial console${within ? ` within ${within}` : ''}`,
      stopped: 'The copy stopped before it booted',
      net: (type: string, within: string | null) => `The copy did not answer on ${type}${within ? ` within ${within}` : ''}`,
      any: 'The copy did not boot',
    },
    stableOk: 'Kept answering after every check passed',
    stableNo: 'Stopped answering during the test: it restarted or hung',
    imageOk: 'The backup image was not modified by the test',
    imageNo: 'The restored image changed during the test',
    probeOk: (type: string) => `Answered on ${type}`,
    probeNo: (type: string) => `Did not answer on ${type}`,
    unknown: 'Could not decide',
  },
  checkTypes: { tcp: 'TCP', http: 'HTTP', rdp: 'RDP', winrm: 'WinRM', ssh: 'SSH', boot_serial: 'the serial console' } as Record<string, string>,
  rtoOf: (rto: string, restore: string | null) => `Proven RTO of this test: ${rto}${restore ? ` (restore ${restore}, then boot and checks)` : ''}.`,
  testDetails: 'Technical details of this test',
  shotBoot: 'at boot',
  shotEnd: 'end of test',
  shotAlt: (name: string) => `Console of the restored copy, ${name}`,
  noShots: 'This test’s bundle has no screenshots.',

  // points
  pointsTable: 'Points',
  pointsHint: (kept: number, pruned: number) => `${plural(kept, 'point', 'points')} kept${pruned ? `, ${n(pruned)} pruned by retention or forgotten` : ''}`,
  noPoints: 'No backup yet.',
  col: { point: 'Point', taken: 'Taken', kind: 'Kind', size: 'Size', proof: 'Proof', detail: 'Detail', action: 'Action', when: 'When', change: 'Change', lasted: 'Lasted', why: 'Why' },
  pnote: {
    returnHere: 'return here',
    notTested: 'not tested',
    checkPending: 'check pending',
    damaged: 'check failed',
    testing: 'being tested',
    couldNotRun: 'test could not run',
    warned: 'test warned',
  },
  pdetail: {
    proven: (test: number, rto: string | null) => `test ${test}${rto ? ` · ${rto}` : ''}`,
    failed: (test: number, why: string) => `test ${test} · ${why}`,
    failedAny: 'a check failed',
    noBoot: 'copy did not boot',
    rebooted: 'copy restarted by itself',
    check: (check: string) => `check “${check}” failed`,
    warned: (test: number) => `test ${test} warned`,
    notRun: (test: number) => `test ${test} could not run`,
    testing: (test: number) => `test ${test} is restoring it now`,
    damaged: 'not a point you can return to',
    checkPending: 'its check has not run yet',
  },
  forget: 'Forget…',
  forgetWhat: (id: number) => `Point ${id} is deleted from its repository and marked pruned, as retention would. This cannot be undone; the reason goes to the audit log.`,
  forgetReason: 'Reason',
  forgetConfirm: (id: number) => `Forget point ${id}`,
  forgotten: (id: number) => `Point ${id} was forgotten.`,
  cancel: 'Cancel',

  // status history
  historyTable: 'Status history',
  added: 'added',
  to: 'to',
  soFar: (d: string) => `${d} so far`,
  noHistory: 'No status change recorded yet.',
  why: {
    proven: 'A restore test passed.',
    test_failed_boot: 'A restore test failed: the copy did not boot.',
    guest_rebooted: 'A restore test failed: the copy restarted by itself.',
    test_failed_check: 'A restore test failed a check after boot.',
    no_backup_in_rpo: 'No backup within the RPO target.',
    host_unreachable: 'The host stopped answering over SSH.',
    host_key_untrusted: 'The host’s SSH key is not trusted yet.',
    host_key_changed: 'The host presented a different SSH key than the pinned one.',
    backup_failing: 'The newest backup failed.',
    repo_content_damaged: 'A check found a kept point damaged.',
    no_policy: 'The VM has no policy.',
    never_backed_up: 'No backup had passed its check yet.',
    never_proven: 'No restore test had passed yet.',
    no_proven_rto: 'A restore test passed without measuring the restore time.',
    proven_rpo_over_target: 'The proven point became older than the RPO target.',
    rto_over_target: 'The restore took longer than the RTO target.',
    proof_too_old: 'The proof became older than its maximum age.',
    no_rule: 'No rule of the status model applied (a fault in Gaia).',
  } as Record<string, string>,
  whyOther: (title: string) => `${title}.`,
  whyNoCode: 'Recorded before reasons had codes; the words are under Technical details.',
  changeDetails: 'Technical details of this change',

  // evidence
  ev: {
    none: 'No restore test has sealed evidence yet.',
    of: (test: number, at: string) => `Evidence of test ${test} (${at})`,
    bundle: 'Bundle',
    signedBy: 'Signed by',
    ledger: 'In the ledger',
    anchored: 'Anchored',
    verified: 'VERIFIED',
    invalid: 'DOES NOT VERIFY',
    notChecked: 'not checked now',
    files: (k: number) => `all ${plural(k, 'file', 'files')} match the signed manifest`,
    entry: (seq: number, checked: string | null) => `entry ${n(seq)}${checked ? ` · checked ${checked}` : ' · not checked yet'}`,
    notInLedger: 'not in the ledger',
    yes: (at: string) => `in the daily report of ${at}`,
    notYet: 'not yet: the next daily report will include it',
    problems: 'Technical details of the check',
    noBundle: 'This test sealed no bundle.',
  },
}
