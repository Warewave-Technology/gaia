package status

import (
	"fmt"
	"strings"
	"time"
)

// Code is a reason's stable machine name (ADR-007 C3). The list below is closed: Compute gives no reason without one,
// and docs/reason-codes.md is generated from it (TestReasonCodesDoc). Codes are part of the API contract.
type Code string

// CodesVersion is the version of the list; it grows when a code is added, changed or removed.
const CodesVersion = 5

const (
	// CRITICAL: the newest checked backup is older than the RPO target (rule theoretical_rpo_over_target). The code
	// says why, when the newest backup job tells.
	NoBackupInRPO   Code = "no_backup_in_rpo"
	HostUnreachable Code = "host_unreachable"
	BackupFailing   Code = "backup_failing"
	// CRITICAL (no backup within the RPO target) or UNKNOWN (no checked point): the host's SSH key was never trusted,
	// so Gaia does not connect to it (ADR-007 C11). Scoped to the host: one cause for all its VMs.
	HostKeyUntrusted Code = "host_key_untrusted"
	// WARNING (backups paused: the newest checked backup is still within the RPO target), CRITICAL (no backup within
	// the RPO target) or UNKNOWN (no checked point): the host presents an SSH key other than the pinned one, so Gaia
	// does not connect to it until an admin checks and accepts the new key (ADR-007 C17, C18). Scoped to the host;
	// never backup_failing. Its group in "Needs attention" is CRITICAL at once.
	HostKeyChanged Code = "host_key_changed"
	// CRITICAL: the latest restore test with a verdict is CRITICAL (rule latest_test_critical), by its failed check.
	TestFailedBoot  Code = "test_failed_boot"
	GuestRebooted   Code = "guest_rebooted"
	TestFailedCheck Code = "test_failed_check"
	// CRITICAL: a present point failed its check (rule integrity_failed).
	RepoContentDamaged Code = "repo_content_damaged"
	// UNKNOWN: a missing input.
	NoPolicy      Code = "no_policy"
	NeverBackedUp Code = "never_backed_up"
	NeverProven   Code = "never_proven"
	NoProvenRTO   Code = "no_proven_rto"
	// WARNING: the proof misses a target.
	ProvenRPOOverTarget Code = "proven_rpo_over_target"
	RTOOverTarget       Code = "rto_over_target"
	ProofTooOld         Code = "proof_too_old"
	// OK.
	Proven Code = "proven"
	// UNKNOWN: no row applied (cannot happen; never healthy).
	NoRule Code = "no_rule"
	// Notes: facts that do not change the status.
	TestWarning Code = "test_warning"
	FullBackup  Code = "full_backup"
	TestNotRun  Code = "test_not_run"
	// The latest restore test is WARNING because the restored guest restarted by itself before it kept answering
	// (ADR-005 F): a backup finished its first boot long ago.
	GuestRestartedDuringBoot Code = "guest_restarted_during_boot"
	// A migration check's readiness, not a VM's status (ADR-005 C): CRITICAL, the converted Windows restarts on every
	// boot because a virtio-win driver fails to install at first boot.
	DriverInstallLoop Code = "driver_install_loop"
)

// RuleBackupsPaused is the truth-table row of a VM whose host's SSH key changed while its newest checked backup is
// within the RPO target: Gaia reads nothing from the host, so its backups are paused (WARNING, ADR-007 C18).
const RuleBackupsPaused = "backups_paused"

// SubjectMigrationCheck marks a code given to a migration check's readiness, not to a protected VM's status: it has no
// row of the status table.
const SubjectMigrationCheck = "migration_check"

// Scope kinds: the thing a reason's cause is shared by. A reason without a scope is the VM's own.
const (
	ScopeHost       = "host"
	ScopeRepository = "repository"
	ScopeSandbox    = "sandbox"
)

// Scope is the shared cause of a reason: one host that does not answer is one cause for all its VMs.
type Scope struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Params are a reason's facts. Times are absolute, so a stored reason reads right later (Render); which fields a code
// sets is in its Def.
type Params struct {
	Point   int64      `json:"point,omitempty"`
	PointAt *time.Time `json:"point_at,omitempty"`
	Points  []int64    `json:"points,omitempty"`
	Test    int64      `json:"test,omitempty"`
	TestAt  *time.Time `json:"test_at,omitempty"`
	Job     int64      `json:"job,omitempty"`
	JobAt   *time.Time `json:"job_at,omitempty"`
	Check   string     `json:"check,omitempty"`
	Detail  string     `json:"detail,omitempty"`
	TargetS int64      `json:"target_s,omitempty"`
	// host_key_changed: when the key Gaia trusts was pinned, and the fingerprint of the different key the host presents.
	PinnedAt    *time.Time `json:"pinned_at,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	// driver_install_loop: the virtio-win drivers whose first-boot install failed on every boot
	Drivers []string `json:"drivers,omitempty"`
	RTOS    *float64 `json:"rto_s,omitempty"`
	// for the OK sentence
	RPOTargetS   int64 `json:"rpo_target_s,omitempty"`
	RTOTargetS   int64 `json:"rto_target_s,omitempty"`
	MaxProofAgeS int64 `json:"max_proof_age_s,omitempty"`
}

// Variant is another status and truth-table row a code can be given in, besides its Def's own.
type Variant struct {
	Status string
	Rule   string
}

// Def is one code: what it means, which status and truth-table row it belongs to, and its sentence.
type Def struct {
	Code   Code
	Status string // OK, WARNING, CRITICAL, UNKNOWN; empty for a note
	Rule   string // the truth-table row; empty for a note
	// Also: the other rows the code is given in (host_key_untrusted: CRITICAL when backups stopped, UNKNOWN when the
	// VM never had a checked point).
	Also  []Variant
	Scope string // the scope kind it can carry; empty: the VM's own
	// GroupStatus: the status of the code's group in "Needs attention" when it is worse than its VMs' (a host whose
	// key changed is a CRITICAL cause at once while its VMs are WARNING, ADR-007 C18); empty: the worst of its VMs.
	GroupStatus string
	// Subject: what the code is given to when not a protected VM's status (SubjectMigrationCheck); such a code has no
	// rule.
	Subject string
	Params  []string
	Cause   string // the cause in a few words, for a group of VMs ("Restore test failed to boot")
	Means   string // one line for the documentation
	text    func(p Params, sc *Scope, now time.Time) string
}

func since(t *time.Time, now time.Time) string {
	if t == nil {
		return "?"
	}
	return dur(now.Sub(*t).Seconds())
}

func clock(t *time.Time) string {
	if t == nil {
		return "?"
	}
	return t.UTC().Format(time.RFC3339)
}

func scopeName(sc *Scope) string {
	if sc == nil {
		return "?"
	}
	return sc.Name
}

var rpoOver = func(p Params, now time.Time) string {
	return fmt.Sprintf("the newest checked backup (point %d) is %s old, over the RPO target %s", p.Point, since(p.PointAt, now), dur(float64(p.TargetS)))
}

var latestCritical = func(p Params, _ *Scope, _ time.Time) string {
	return fmt.Sprintf("the latest restore test (%d, point %d) is CRITICAL: %s", p.Test, p.Point, p.Detail)
}

var defs = []Def{
	{Code: NoBackupInRPO, Status: Critical, Rule: "theoretical_rpo_over_target", Params: []string{"point", "point_at", "target_s"},
		Cause: "No backup within the RPO target", Means: "the newest checked backup is older than the RPO target, and the newest backup job did not fail",
		text: func(p Params, _ *Scope, now time.Time) string { return rpoOver(p, now) }},
	{Code: HostUnreachable, Status: Critical, Rule: "theoretical_rpo_over_target", Scope: ScopeHost, Params: []string{"point", "point_at", "target_s", "job", "job_at", "detail"},
		Cause: "Host does not answer", Means: "no backup within the RPO target because the newest backup job could not reach the host over SSH",
		text: func(p Params, sc *Scope, now time.Time) string {
			return fmt.Sprintf("%s: host %s does not answer (backup job %d at %s: %s)", rpoOver(p, now), scopeName(sc), p.Job, clock(p.JobAt), p.Detail)
		}},
	{Code: HostKeyUntrusted, Status: Critical, Rule: "theoretical_rpo_over_target", Also: []Variant{{Status: Unknown, Rule: "no_checked_point"}},
		Scope: ScopeHost, Params: []string{"point", "point_at", "target_s", "job", "job_at", "detail"},
		Cause: "Host key not trusted yet",
		Means: "the host's SSH key was never trusted, so Gaia does not connect to it: no backup within the RPO target (CRITICAL), or no checked backup at all (UNKNOWN, rule `no_checked_point`)",
		text: func(p Params, sc *Scope, now time.Time) string {
			s := fmt.Sprintf("host %s's SSH key is not trusted yet, so Gaia does not connect to it (scan the host and trust its fingerprint)", scopeName(sc))
			if p.Job != 0 {
				s += fmt.Sprintf(" (backup job %d at %s)", p.Job, clock(p.JobAt))
			}
			if p.PointAt == nil {
				return "there is no backup point that passed its post-backup check: " + s
			}
			return rpoOver(p, now) + ": " + s
		}},
	{Code: HostKeyChanged, Status: Critical, Rule: "theoretical_rpo_over_target",
		Also:  []Variant{{Status: Unknown, Rule: "no_checked_point"}, {Status: Warning, Rule: RuleBackupsPaused}},
		Scope: ScopeHost, GroupStatus: Critical, Params: []string{"point", "point_at", "target_s", "job", "job_at", "detail", "pinned_at", "fingerprint"},
		Cause: "Host presents a different SSH key",
		Means: "the host presents an SSH host key other than the pinned one, so Gaia reads nothing from it until an admin checks and accepts the new key (never automatically): backups paused while the newest checked backup is within the RPO target (WARNING, rule `backups_paused`, from the moment the change is seen), no backup within the RPO target (CRITICAL), or no checked backup at all (UNKNOWN, rule `no_checked_point`); its group in Needs attention is CRITICAL at once",
		text: func(p Params, sc *Scope, now time.Time) string {
			s := fmt.Sprintf("host %s presents a different SSH host key (%s) than the one pinned on %s; Gaia reads nothing from it until an admin checks and accepts the new key",
				scopeName(sc), p.Fingerprint, clock(p.PinnedAt))
			if p.Job != 0 {
				s += fmt.Sprintf(" (backup job %d at %s)", p.Job, clock(p.JobAt))
			}
			switch {
			case p.PointAt == nil:
				return "there is no backup point that passed its post-backup check: " + s
			case p.TargetS == 0:
				return fmt.Sprintf("%s: backups are paused (the newest checked backup, point %d, is %s old)", s, p.Point, since(p.PointAt, now))
			case now.Sub(*p.PointAt) <= time.Duration(p.TargetS)*time.Second:
				return fmt.Sprintf("%s: backups are paused (the newest checked backup, point %d, is %s old, within the RPO target %s)",
					s, p.Point, since(p.PointAt, now), dur(float64(p.TargetS)))
			}
			return rpoOver(p, now) + ": " + s
		}},
	{Code: BackupFailing, Status: Critical, Rule: "theoretical_rpo_over_target", Params: []string{"point", "point_at", "target_s", "job", "job_at", "detail"},
		Cause: "Backups fail", Means: "no backup within the RPO target because the newest backup job failed (for a reason other than the host not answering or its SSH key)",
		text: func(p Params, _ *Scope, now time.Time) string {
			return fmt.Sprintf("%s: backup job %d failed at %s: %s", rpoOver(p, now), p.Job, clock(p.JobAt), p.Detail)
		}},
	{Code: TestFailedBoot, Status: Critical, Rule: "latest_test_critical", Params: []string{"test", "point", "test_at", "check", "detail"},
		Cause: "Restore test failed to boot", Means: "the latest restore test with a verdict is CRITICAL because the restored guest did not boot",
		text: latestCritical},
	{Code: GuestRebooted, Status: Critical, Rule: "latest_test_critical", Params: []string{"test", "point", "test_at", "check", "detail"},
		Cause: "Restored guest restarts by itself", Means: "the latest restore test with a verdict is CRITICAL because the guest stopped answering inside the stability window (ADR-005 B)",
		text: latestCritical},
	{Code: TestFailedCheck, Status: Critical, Rule: "latest_test_critical", Params: []string{"test", "point", "test_at", "check", "detail"},
		Cause: "Restore test check failed", Means: "the latest restore test with a verdict is CRITICAL because a check after boot failed (the check is named)",
		text: latestCritical},
	{Code: RepoContentDamaged, Status: Critical, Rule: "integrity_failed", Scope: ScopeRepository, Params: []string{"points"},
		Cause: "Repository content damaged", Means: "present points failed their check (after the backup, or a repository verification found damaged contents)",
		text: func(p Params, sc *Scope, _ time.Time) string {
			s := fmt.Sprintf("integrity check failed for point(s) %v", p.Points)
			if sc != nil {
				s += " in repository " + sc.Name
			}
			return s
		}},
	{Code: NoPolicy, Status: Unknown, Rule: "no_policy", Cause: "No policy", Means: "the VM has no policy, so it has no targets",
		text: func(Params, *Scope, time.Time) string { return "the VM has no policy, so it has no targets" }},
	{Code: NeverBackedUp, Status: Unknown, Rule: "no_checked_point", Cause: "Never backed up", Means: "no present point passed its post-backup check",
		text: func(Params, *Scope, time.Time) string {
			return "there is no backup point that passed its post-backup check"
		}},
	{Code: NeverProven, Status: Unknown, Rule: "no_passing_test", Cause: "Never proven", Means: "no restore test of a present, undamaged point has passed",
		text: func(Params, *Scope, time.Time) string { return "no restore test of this VM has ever passed" }},
	{Code: NoProvenRTO, Status: Unknown, Rule: "no_proven_rto", Params: []string{"test"}, Cause: "Passed without a proven RTO", Means: "the newest passing test has no proven RTO",
		text: func(p Params, _ *Scope, _ time.Time) string {
			return fmt.Sprintf("restore test %d passed but has no proven RTO", p.Test)
		}},
	{Code: ProvenRPOOverTarget, Status: Warning, Rule: "proven_rpo_over_target", Params: []string{"test", "point", "point_at", "target_s"},
		Cause: "Proven point older than the RPO target", Means: "backups are within the RPO target, but the newest proven point is not",
		text: func(p Params, _ *Scope, now time.Time) string {
			return fmt.Sprintf("the newest proven point (%d, test %d) is %s old, over the RPO target %s", p.Point, p.Test, since(p.PointAt, now), dur(float64(p.TargetS)))
		}},
	{Code: RTOOverTarget, Status: Warning, Rule: "proven_rto_over_target", Params: []string{"test", "rto_s", "target_s"},
		Cause: "Proven RTO over target", Means: "the newest passing test restored and booted the VM slower than the RTO target",
		text: func(p Params, _ *Scope, _ time.Time) string {
			rto := 0.0
			if p.RTOS != nil {
				rto = *p.RTOS
			}
			return fmt.Sprintf("the proven RTO %s (test %d) is over the RTO target %s", dur(rto), p.Test, dur(float64(p.TargetS)))
		}},
	{Code: ProofTooOld, Status: Warning, Rule: "proof_too_old", Params: []string{"test", "test_at", "target_s"},
		Cause: "Proof older than its maximum age", Means: "the newest passing test is older than the policy's maximum proof age",
		text: func(p Params, _ *Scope, now time.Time) string {
			return fmt.Sprintf("the proof (test %d) is %s old, over the maximum %s", p.Test, since(p.TestAt, now), dur(float64(p.TargetS)))
		}},
	{Code: Proven, Status: OK, Rule: "proven", Params: []string{"test", "point", "point_at", "test_at", "rto_s", "rpo_target_s", "rto_target_s", "max_proof_age_s"},
		Cause: "Proven", Means: "the newest passing test meets every target",
		text: func(p Params, _ *Scope, now time.Time) string {
			rto := 0.0
			if p.RTOS != nil {
				rto = *p.RTOS
			}
			return fmt.Sprintf("point %d is proven by test %d: proven RPO %s <= %s, proven RTO %s <= %s, proof age %s <= %s",
				p.Point, p.Test, since(p.PointAt, now), dur(float64(p.RPOTargetS)), dur(rto), dur(float64(p.RTOTargetS)),
				since(p.TestAt, now), dur(float64(p.MaxProofAgeS)))
		}},
	{Code: NoRule, Status: Unknown, Rule: "no_rule", Cause: "No rule applied", Means: "no row of the status table applied (cannot happen; never shown as healthy)",
		text: func(Params, *Scope, time.Time) string { return "no row of the status table applied" }},
	{Code: TestWarning, Params: []string{"test", "point", "detail"}, Cause: "Restore test warned", Means: "note: the latest restore test with a verdict is WARNING",
		text: func(p Params, _ *Scope, _ time.Time) string {
			return fmt.Sprintf("the latest restore test (%d, point %d) is WARNING: %s", p.Test, p.Point, p.Detail)
		}},
	{Code: GuestRestartedDuringBoot, Params: []string{"test", "point", "check", "detail"}, Cause: "Restored guest restarted while booting",
		Means: "note: the latest restore test is WARNING because the restored guest restarted by itself before it kept answering for settle_s (ADR-005 F)",
		text: func(p Params, _ *Scope, _ time.Time) string {
			return fmt.Sprintf("the latest restore test (%d, point %d) is WARNING: the restored guest restarted while it booted: %s", p.Test, p.Point, p.Detail)
		}},
	{Code: FullBackup, Params: []string{"point", "detail"}, Cause: "Full backup for a reason", Means: "note: the newest backup is full although the VM had an earlier one (a checkpoint gone, a disk resized, a quarantine)",
		text: func(p Params, _ *Scope, _ time.Time) string {
			return fmt.Sprintf("the newest backup (point %d) is a full backup: %s", p.Point, p.Detail)
		}},
	{Code: DriverInstallLoop, Status: Critical, Subject: SubjectMigrationCheck, Params: []string{"drivers", "detail"},
		Cause: "Converted Windows restarts for a driver", Means: "a migration check: the converted Windows restarts on every boot because virt-v2v's first-boot service cannot install a virtio-win driver (ADR-005 C); checked again with exclude_drivers it can be WARNING at best",
		text: func(p Params, _ *Scope, _ time.Time) string {
			return fmt.Sprintf("the converted Windows restarts on every boot: virtio-win's %s driver fails to install at first boot (%s)",
				strings.Join(p.Drivers, ", "), p.Detail)
		}},
	{Code: TestNotRun, Scope: ScopeSandbox, Params: []string{"test", "point", "detail"}, Cause: "Restore test could not run",
		Means: "note: the newest restore test could not run (the sandbox scope when the sandbox helper did not answer)",
		text: func(p Params, _ *Scope, _ time.Time) string {
			return fmt.Sprintf("restore test %d (point %d) could not run: %s", p.Test, p.Point, p.Detail)
		}},
}

var byCode = func() map[Code]Def {
	m := map[Code]Def{}
	for _, d := range defs {
		if _, dup := m[d.Code]; dup {
			panic("status: code " + string(d.Code) + " defined twice")
		}
		m[d.Code] = d
	}
	return m
}()

// Defs lists every code, in the documentation's order.
func Defs() []Def { return defs }

// Allows says whether the code is given with this status in this truth-table row.
func (d Def) Allows(status, rule string) bool {
	if d.Status == status && d.Rule == rule {
		return true
	}
	for _, v := range d.Also {
		if v.Status == status && v.Rule == rule {
			return true
		}
	}
	return false
}

// Lookup finds a code's definition.
func Lookup(c Code) (Def, bool) {
	d, ok := byCode[c]
	return d, ok
}

// Render writes a reason's sentence for now: a stored reason's ages grow with the clock.
func Render(r Reason, now time.Time) string {
	d, ok := byCode[r.Code]
	if !ok {
		return r.Text // a reason from a newer version: keep what it said
	}
	return d.text(r.Params, r.Scope, now)
}
