// Package status computes a protected VM's status: the answer to "which point can I return to right now, and is
// that proven?" (ADR-007, "The status model"). Compute is the only place the truth table lives.
package status

import (
	"cmp"
	"slices"
	"time"
)

// Statuses, worst last: CRITICAL > UNKNOWN > WARNING > OK (ADR-007).
const (
	OK       = "OK"
	Warning  = "WARNING"
	Unknown  = "UNKNOWN"
	Critical = "CRITICAL"
)

var rank = map[string]int{OK: 0, Warning: 1, Unknown: 2, Critical: 3}

// Targets are a policy's targets.
type Targets struct {
	RPOS         int64 `json:"rpo_target_s"`
	RTOS         int64 `json:"rto_target_s"`
	MaxProofAgeS int64 `json:"max_proof_age_s"`
}

// Point is a backup point that passed its post-backup check.
type Point struct {
	ID      int64     `json:"id"`
	TakenAt time.Time `json:"taken_at"`
	// FullReason says why the point is a full backup when the VM had an earlier one (a checkpoint missing on the host,
	// a disk resized, a quarantine); empty for an incremental or a VM's first backup.
	FullReason string `json:"full_reason,omitempty"`
}

// Test is a finished restore test.
type Test struct {
	ID           int64     `json:"id"`
	PointID      int64     `json:"point_id"`
	PointTakenAt time.Time `json:"point_taken_at"`
	Verdict      string    `json:"verdict"` // OK, WARNING, CRITICAL, UNKNOWN
	FinishedAt   time.Time `json:"finished_at"`
	ProvenRTOS   *float64  `json:"proven_rto_s"` // restore + ready (ADR-004 A4); set when the test passed
	Detail       string    `json:"detail"`       // what failed, for a test that did not pass
	FailedCheck  string    `json:"failed_check"` // the first CRITICAL check (boot, stable, ssh, ...), for a CRITICAL test
	SandboxDown  bool      `json:"sandbox_down"` // a test that could not run because the sandbox helper did not answer
}

// Backup is the newest finished backup job, when it failed after the newest checked point.
type Backup struct {
	JobID       int64     `json:"job_id"`
	FinishedAt  time.Time `json:"finished_at"`
	Error       string    `json:"error"`
	Unreachable bool      `json:"unreachable"` // the host did not answer over SSH
}

// Input is everything the status depends on. A nil field is a missing input.
type Input struct {
	Now               time.Time
	Targets           *Targets // nil: the VM has no policy
	NewestChecked     *Point   // the newest present point whose post-backup check passed
	LatestTest        *Test    // the newest restore test that reached a verdict (OK, WARNING or CRITICAL)
	LatestAttempt     *Test    // the newest finished restore test, UNKNOWN included (a test that could not run)
	LatestPassingTest *Test    // the newest restore test with verdict OK
	DamagedPoints     []int64  // present points whose check failed (post-backup check or repository verification)
	// What the reasons name as their shared cause (ADR-007 C3); nil when not known.
	Host         *Scope
	Repository   *Scope
	FailedBackup *Backup // the newest backup job failed after the newest checked point
}

// Reason is one row of the truth table that applied, with the status that row gives, or a note (Status empty).
type Reason struct {
	Status string `json:"status"`
	Rule   string `json:"rule"` // the truth-table row, e.g. theoretical_rpo_over_target
	Code   Code   `json:"code"` // why, from the closed list (codes.go), e.g. host_unreachable
	Scope  *Scope `json:"scope"`
	Params Params `json:"params"`
	Text   string `json:"text"`
}

// Result is a VM's status and the figures behind it.
type Result struct {
	Status         string   `json:"status"`
	TheoreticalRPO *float64 `json:"theoretical_rpo_s"` // now - newest checked point
	ProvenRPO      *float64 `json:"proven_rpo_s"`      // now - point of the newest passing test
	ProvenRTO      *float64 `json:"proven_rto_s"`      // from that test
	ProofAge       *float64 `json:"proof_age_s"`       // now - that test
	Reasons        []Reason `json:"reasons"`           // worst first; the first one says why the status is what it is
	Notes          []string `json:"notes"`             // facts that do not change the status (the text of NoteReasons)
	NoteReasons    []Reason `json:"note_reasons"`      // the same notes with their codes
}

// Compute applies ADR-007's truth table:
//
//	OK        proven RPO <= RPO target and proven RTO <= RTO target and proof age <= maximum
//	WARNING   theoretical RPO within target, but proof misses a target (proven RPO, RTO or proof age)
//	CRITICAL  theoretical RPO over target, or the latest restore test is CRITICAL, or integrity verification failed
//	UNKNOWN   no restore test ever passed, or any input is missing
//
// Every row that applies is a reason; the worst status wins. UNKNOWN is never shown as healthy.
//
// "The latest restore test" is the latest that reached a verdict: a test that could not run (UNKNOWN: the sandbox
// was down, the restore failed) proves nothing either way, so it neither clears a CRITICAL nor replaces a pass. It
// is listed in Notes.
func Compute(in Input) Result {
	r := Result{Reasons: []Reason{}, Notes: []string{}, NoteReasons: []Reason{}}
	mk := func(c Code, sc *Scope, p Params) Reason {
		d, ok := byCode[c]
		if !ok {
			panic("status: no code " + string(c)) // a typo in this file; TestReasonCodes covers every code
		}
		return Reason{Status: d.Status, Rule: d.Rule, Code: c, Scope: sc, Params: p, Text: d.text(p, sc, in.Now)}
	}
	add := func(c Code, sc *Scope, p Params) { r.Reasons = append(r.Reasons, mk(c, sc, p)) }
	note := func(c Code, sc *Scope, p Params) {
		x := mk(c, sc, p)
		r.NoteReasons, r.Notes = append(r.NoteReasons, x), append(r.Notes, x.Text)
	}
	secs := func(t time.Time) *float64 { s := in.Now.Sub(t).Seconds(); return &s }
	at := func(t time.Time) *time.Time { return &t }
	t := in.Targets

	if in.NewestChecked != nil {
		r.TheoreticalRPO = secs(in.NewestChecked.TakenAt)
	}
	if p := in.LatestPassingTest; p != nil {
		r.ProvenRPO, r.ProofAge, r.ProvenRTO = secs(p.PointTakenAt), secs(p.FinishedAt), p.ProvenRTOS
	}

	// CRITICAL rows.
	if t != nil && r.TheoreticalRPO != nil && *r.TheoreticalRPO > float64(t.RPOS) {
		p := Params{Point: in.NewestChecked.ID, PointAt: at(in.NewestChecked.TakenAt), TargetS: t.RPOS}
		switch b := in.FailedBackup; {
		case b != nil && b.Unreachable && in.Host != nil:
			p.Job, p.JobAt, p.Detail = b.JobID, at(b.FinishedAt), b.Error
			add(HostUnreachable, in.Host, p)
		case b != nil:
			p.Job, p.JobAt, p.Detail = b.JobID, at(b.FinishedAt), b.Error
			add(BackupFailing, nil, p)
		default:
			add(NoBackupInRPO, nil, p)
		}
	}
	if lt := in.LatestTest; lt != nil && lt.Verdict == Critical {
		code := TestFailedCheck
		switch lt.FailedCheck {
		case "boot":
			code = TestFailedBoot
		case "stable":
			code = GuestRebooted
		}
		add(code, nil, Params{Test: lt.ID, Point: lt.PointID, TestAt: at(lt.FinishedAt), Check: lt.FailedCheck, Detail: lt.Detail})
	}
	if len(in.DamagedPoints) > 0 {
		add(RepoContentDamaged, in.Repository, Params{Points: in.DamagedPoints})
	}

	// UNKNOWN rows: a missing input.
	if t == nil {
		add(NoPolicy, nil, Params{})
	}
	if in.NewestChecked == nil {
		add(NeverBackedUp, nil, Params{})
	}
	if in.LatestPassingTest == nil {
		add(NeverProven, nil, Params{})
	} else if r.ProvenRTO == nil {
		add(NoProvenRTO, nil, Params{Test: in.LatestPassingTest.ID})
	}

	// WARNING and OK rows need the targets, a theoretical RPO within target, and a complete proof.
	if t != nil && r.TheoreticalRPO != nil && *r.TheoreticalRPO <= float64(t.RPOS) && r.ProvenRPO != nil && r.ProvenRTO != nil {
		p := in.LatestPassingTest
		missed := false
		if *r.ProvenRPO > float64(t.RPOS) {
			missed = true
			add(ProvenRPOOverTarget, nil, Params{Test: p.ID, Point: p.PointID, PointAt: at(p.PointTakenAt), TargetS: t.RPOS})
		}
		if *r.ProvenRTO > float64(t.RTOS) {
			missed = true
			add(RTOOverTarget, nil, Params{Test: p.ID, RTOS: p.ProvenRTOS, TargetS: t.RTOS})
		}
		if *r.ProofAge > float64(t.MaxProofAgeS) {
			missed = true
			add(ProofTooOld, nil, Params{Test: p.ID, TestAt: at(p.FinishedAt), TargetS: t.MaxProofAgeS})
		}
		if !missed {
			add(Proven, nil, Params{Test: p.ID, Point: p.PointID, PointAt: at(p.PointTakenAt), TestAt: at(p.FinishedAt), RTOS: p.ProvenRTOS,
				RPOTargetS: t.RPOS, RTOTargetS: t.RTOS, MaxProofAgeS: t.MaxProofAgeS})
		}
	}

	if lt := in.LatestTest; lt != nil && lt.Verdict == Warning {
		note(TestWarning, nil, Params{Test: lt.ID, Point: lt.PointID, Detail: lt.Detail})
	}
	if p := in.NewestChecked; p != nil && p.FullReason != "" {
		note(FullBackup, nil, Params{Point: p.ID, Detail: p.FullReason})
	}
	if a := in.LatestAttempt; a != nil && a.Verdict == Unknown && (in.LatestTest == nil || a.ID > in.LatestTest.ID) {
		var sc *Scope
		if a.SandboxDown {
			sc = &Scope{Kind: ScopeSandbox, Name: "local"} // the Gaia server's own sandbox (ADR-007 C1)
		}
		note(TestNotRun, sc, Params{Test: a.ID, Point: a.PointID, Detail: a.Detail})
	}
	if len(r.Reasons) == 0 { // cannot happen with the rows above; never default to healthy
		add(NoRule, nil, Params{})
	}
	slices.SortStableFunc(r.Reasons, func(a, b Reason) int { return cmp.Compare(rank[b.Status], rank[a.Status]) })
	r.Status = r.Reasons[0].Status
	return r
}

// NextChange is when time alone next changes the status or its reasons, after in.Now: a checked backup, a proven point
// or a proof crossing its target. Nil when no clock-driven change is ahead. The candidates are tried with Compute
// itself, so a crossing that changes nothing (the VM is UNKNOWN for another reason) is not one.
func NextChange(in Input) *time.Time {
	t := in.Targets
	if t == nil {
		return nil
	}
	var cand []time.Time
	if p := in.NewestChecked; p != nil {
		cand = append(cand, p.TakenAt.Add(time.Duration(t.RPOS)*time.Second))
	}
	if p := in.LatestPassingTest; p != nil {
		cand = append(cand, p.PointTakenAt.Add(time.Duration(t.RPOS)*time.Second), p.FinishedAt.Add(time.Duration(t.MaxProofAgeS)*time.Second))
	}
	slices.SortFunc(cand, func(a, b time.Time) int { return a.Compare(b) })
	base := signature(Compute(in))
	for _, x := range cand {
		if !x.After(in.Now) {
			continue
		}
		after := in
		after.Now = x.Add(time.Nanosecond) // a target is crossed when the age is over it, not at it
		if signature(Compute(after)) != base {
			return &x
		}
	}
	return nil
}

// signature is what a clock-driven change changes: the status and the codes of its reasons.
func signature(r Result) string {
	s := r.Status
	for _, x := range r.Reasons {
		s += " " + string(x.Code)
	}
	return s
}

// dur renders seconds for a reason: 90 -> "1m30s".
func dur(s float64) string {
	return time.Duration(s * float64(time.Second)).Round(time.Second).String()
}
