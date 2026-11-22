// Sentences from reason codes (ADR-007 C3, C11, C15; docs/design/v3 rule 3). The server sends a code, its parameters
// and its scope; the screen says it as an operator would. Raw text (a probe's output, an error string) is never in
// these sentences: it is `detail`, shown under "Technical details".
import { dur } from '../lib/duration.ts'

/** What the screen knows about a reason: the code, its parameters (times as RFC 3339), its scope. */
export type ReasonIn = {
  code: string
  params?: {
    point?: number
    point_at?: string | null
    points?: number[] | null
    test?: number
    test_at?: string | null
    job?: number
    job_at?: string | null
    check?: string
    detail?: string
    target_s?: number
    rto_s?: number | null
    rpo_target_s?: number
    rto_target_s?: number
    max_proof_age_s?: number
  } | null
  scope?: { kind: string; id: number; name: string } | null
}

/** A cause shared by many VMs, as the overview's "Needs attention" row shows it. */
export type CauseIn = ReasonIn & {
  count: number
  /** When the oldest VM of the group got this cause, as the screen writes it ("09:12", "yesterday 22:51"). */
  since: string
  hosts: { name: string; count: number }[]
  policies: { name: string; count: number }[]
}

/** Seconds from an RFC 3339 time to now. */
const age = (iso: string | null | undefined, now: number) => (iso ? Math.max(0, (now - Date.parse(iso)) / 1000) : null)
const vms = (n: number) => `${n.toLocaleString('en-US')} ${n === 1 ? 'VM' : 'VMs'}`
const its = (n: number) => (n === 1 ? 'Its VM' : `Its ${vms(n)}`)
const their = (n: number) => (n === 1 ? 'This VM' : `These ${vms(n)}`)
const name = (r: ReasonIn, fallback: string) => r.scope?.name ?? fallback

/** The check a restore test runs, in words (internal/verify's check names). */
export function checkName(check: string | undefined): string {
  switch (check) {
    case 'boot': return 'boot'
    case 'stable': return 'stays up'
    case 'unchanged': return 'image unchanged'
    case 'integrity': return 'integrity'
    case '': case undefined: return 'a check'
    default: return check.replaceAll('_', ' ')
  }
}

type Def = {
  /** The cause in a few words: a cause row's title, the VM page's "why" heading. */
  title: string
  /** One VM's reason, short enough for a table cell ("Proof 8 h 10 min old, max 6 h"). */
  vm: (r: ReasonIn, now: number) => string
  /** A cause row's summary beside its title. */
  short: (c: CauseIn, now: number) => string
  /** A cause row's whole sentence, when the row is opened. */
  long: (c: CauseIn, now: number) => string
  /** What to do about it, in one or two sentences. */
  todo: string
}

const target = (s: number | undefined) => (s ? `target ${dur(s)}` : 'no target')
// The server sends a cause's busiest hosts only (a short list): name them only when they hold all of its VMs.
const where = (c: CauseIn) => {
  if (c.hosts.reduce((n, h) => n + h.count, 0) !== c.count) return ''
  return c.hosts.length === 1 ? `on ${c.hosts[0].name}` : c.hosts.length > 1 ? `on ${c.hosts.length} hosts` : ''
}

export const reasons: Record<string, Def> = {
  no_backup_in_rpo: {
    title: 'No backup within the RPO target',
    vm: (r, now) => (r.params?.point_at ? `Newest backup ${dur(age(r.params.point_at, now))} old, ${target(r.params.target_s)}` : 'No backup within the RPO target'),
    short: () => `newest backups older than the RPO target`,
    long: (c) => `${their(c.count)} have no checked backup within their RPO target, and their newest backup jobs did not fail: the backups are not running often enough.`,
    todo: 'Check the policy’s backup interval against its RPO target, and whether backups wait in the queue (Activity).',
  },
  host_unreachable: {
    title: 'Host does not answer',
    vm: (r) => `Host ${name(r, '?')} does not answer over SSH`,
    short: () => 'SSH does not answer',
    long: (c) => `${name(c, 'The host')} has not answered over SSH since ${c.since}. ${its(c.count)} have no backup within their RPO target.`,
    todo: 'Check that the host is up and that sshd answers from the Gaia server.',
  },
  host_key_untrusted: {
    title: 'Host key not trusted yet',
    vm: (r) => `Host ${name(r, '?')}: its SSH key is not trusted yet`,
    short: () => 'its SSH key was never confirmed',
    long: (c) => `Gaia does not connect to ${name(c, 'this host')} until someone confirms its SSH key, so ${c.count === 1 ? 'its VM is' : `its ${vms(c.count)} are`} not backed up.`,
    todo: 'On Inventory → Hosts, read the host key, compare its fingerprint with the host’s own, and trust it.',
  },
  backup_failing: {
    title: 'Backups fail',
    vm: (r) => `Backups fail${r.params?.job ? ` (job ${r.params.job})` : ''}`,
    short: (c) => `newest backup job failed ${where(c)}`.trim(),
    long: (c) => `The newest backup job of ${c.count === 1 ? 'this VM' : `each of these ${vms(c.count)}`} failed, so ${c.count === 1 ? 'it has' : 'they have'} no backup within the RPO target. The job’s error is under Technical details on each VM.`,
    todo: 'Open one VM and read the failed job’s error; it usually names the cause (disk space, libvirt, a changed VM).',
  },
  test_failed_boot: {
    title: 'Restore test fails to boot',
    vm: () => 'Restore test: the copy did not boot',
    short: () => 'the restored copy does not boot',
    long: (c) => `The newest restore test of ${c.count === 1 ? 'this VM' : `each of these ${vms(c.count)}`} restored the point but the copy did not boot. ${c.count === 1 ? 'It keeps' : 'They keep'} an older proven point, if one exists.`,
    todo: 'Look at the test’s screenshots on the VM page. A change of firmware (BIOS/UEFI) or disk controller on the source is the usual cause.',
  },
  guest_rebooted: {
    title: 'Restored copy restarts by itself',
    vm: () => 'Restore test: the copy restarted by itself',
    short: () => 'the restored copy restarts during the test',
    long: (c) => `The restored copy booted but stopped answering while the test watched it, so ${c.count === 1 ? 'its point is' : 'their points are'} not proven.`,
    todo: 'Look at the end-of-test screenshot on the VM page: a driver installing and rebooting is the usual cause.',
  },
  test_failed_check: {
    title: 'Restore test check failed',
    vm: (r) => `Restore test: check “${checkName(r.params?.check)}” failed`,
    short: () => 'a check after boot failed',
    long: (c) => `The copy booted, but a check after boot failed${c.params?.check ? ` (“${checkName(c.params.check)}”)` : ''}.`,
    todo: 'Open one VM’s latest test: the failed step says what was expected.',
  },
  repo_content_damaged: {
    title: 'Repository content damaged',
    vm: (r) => `${(r.params?.points?.length ?? 0) || 'Some'} ${r.params?.points?.length === 1 ? 'point' : 'points'} damaged${r.scope ? ` in ${r.scope.name}` : ''}`,
    short: (c) => `${c.scope ? `repository ${c.scope.name}: ` : ''}points failed their check`,
    long: (c) => `A check found damaged contents${c.scope ? ` in repository ${c.scope.name}` : ''}. The points that use them are quarantined; the next backup of each VM is a full one.`,
    todo: 'Nothing to do unless it repeats. If it does, check the storage behind the repository.',
  },
  no_policy: {
    title: 'No policy',
    vm: () => 'No policy: nothing to check it against',
    short: () => 'no targets to check against',
    long: (c) => `${their(c.count)} ${c.count === 1 ? 'has' : 'have'} no policy, so Gaia neither backs ${c.count === 1 ? 'it' : 'them'} up nor knows what “fine” would mean.`,
    todo: 'Give each a policy (VM page → More → Policy), or remove it from Gaia.',
  },
  never_backed_up: {
    title: 'Never backed up',
    vm: () => 'Never backed up',
    short: () => 'no backup has passed its check yet',
    long: (c) => `${their(c.count)} ${c.count === 1 ? 'has' : 'have'} no backup that passed its check yet. Nothing about ${c.count === 1 ? 'it' : 'them'} is known.`,
    todo: 'Nothing to do if they were added recently: the first backup is in the queue. Otherwise see Activity.',
  },
  never_proven: {
    title: 'Never proven',
    vm: () => 'Never proven: no restore test has passed yet',
    short: () => 'first restore tests not passed yet',
    long: (c) => `${their(c.count)} ${c.count === 1 ? 'is' : 'are'} backed up, but no restore test has passed yet: nothing is proven.`,
    todo: 'Nothing to do if they are new: their first tests are in the queue (Proof capacity).',
  },
  no_proven_rto: {
    title: 'Passed without a restore time',
    vm: () => 'A restore test passed but measured no restore time',
    short: () => 'test passed, restore time not measured',
    long: () => 'A restore test passed but did not measure how long the restore took, so the RTO is not proven.',
    todo: 'Run a new test of the newest point.',
  },
  proven_rpo_over_target: {
    title: 'Proven point older than the RPO target',
    vm: (r, now) => (r.params?.point_at ? `Proven point ${dur(age(r.params.point_at, now))} old, ${target(r.params.target_s)}` : 'Proven point older than the RPO target'),
    short: () => 'backed up in time, but not proven in time',
    long: (c) => `Backups are within the RPO target, but the newest point a restore test proved is older than it on ${c.count === 1 ? 'this VM' : `these ${vms(c.count)}`}.`,
    todo: 'Test more often, or add sandbox slots: see Proof capacity.',
  },
  rto_over_target: {
    title: 'Restore takes longer than the RTO target',
    vm: (r) => (r.params?.rto_s != null ? `Restore takes ${dur(r.params.rto_s, true)}, ${target(r.params.target_s)}` : 'Restore takes longer than the RTO target'),
    short: () => 'restore slower than the target',
    long: (c) => `The restore tests of ${c.count === 1 ? 'this VM' : `these ${vms(c.count)}`} restore and boot slower than the RTO target.`,
    todo: 'Raise the RTO target to what is real, or speed up restores (repository read speed).',
  },
  proof_too_old: {
    title: 'Proof older than its maximum age',
    vm: (r, now) => (r.params?.test_at ? `Proof ${dur(age(r.params.test_at, now))} old, max ${dur(r.params.target_s ?? null)}` : 'Proof older than its maximum age'),
    short: () => 'waited too long for a restore test',
    long: (c) => `The newest passing test of ${c.count === 1 ? 'this VM' : `these ${vms(c.count)}`} is older than the policy’s maximum proof age.`,
    todo: 'Add sandbox slots or lengthen the test interval: see Proof capacity.',
  },
  proven: {
    title: 'Proven',
    vm: () => 'A restore test passed',
    short: () => 'every target met',
    long: () => 'The newest passing restore test meets every target.',
    todo: '',
  },
  no_rule: {
    title: 'No rule applied',
    vm: () => 'No rule of the status model applied (a fault in Gaia)',
    short: () => 'a fault in Gaia',
    long: () => 'No row of the status model applied. This cannot happen and is never shown as healthy: report it.',
    todo: 'Report it with the server’s log.',
  },
  // notes: facts that do not change the status
  test_warning: {
    title: 'Restore test warned',
    vm: () => 'The latest restore test warned',
    short: () => 'a test warned', long: () => 'The latest restore test passed with a warning.', todo: '',
  },
  full_backup: {
    title: 'Full backup',
    vm: () => 'The newest backup was a full one',
    short: () => 'full backup', long: () => 'The newest backup was a full one although the VM had an earlier one.', todo: '',
  },
  test_not_run: {
    title: 'Restore test could not run',
    vm: () => 'The newest restore test could not run',
    short: () => 'the sandbox did not run the test',
    long: (c) => `The newest restore test of ${c.count === 1 ? 'this VM' : `these ${vms(c.count)}`} could not run, so it proves nothing.`,
    todo: 'Check the sandbox unit on the Gaia server (Proof capacity shows its slots).',
  },
}

const unknownCode = (code: string): Def => ({
  title: code.replaceAll('_', ' '),
  vm: () => code.replaceAll('_', ' '),
  short: () => '', long: () => `Reason ${code} (from a newer Gaia).`, todo: '',
})

/** The definition of a code; a code this screen does not know yet is still said, never dropped. */
export const reason = (code: string): Def => reasons[code] ?? unknownCode(code)

/** One VM's reason as a short sentence. */
export const vmReason = (r: ReasonIn, now: number) => reason(r.code).vm(r, now)
