// Package status computes a protected VM's status: the answer to "which point can I return to right now, and is
// that proven?" (ADR-007, "The status model"). Compute is the only place the truth table lives.
package status

import (
	"cmp"
	"fmt"
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
}

// Reason is one row of the truth table that applied, with the status that row gives.
type Reason struct {
	Status string `json:"status"`
	Rule   string `json:"rule"` // stable name of the condition, e.g. theoretical_rpo_over_target
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
	Notes          []string `json:"notes"`             // facts that do not change the status
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
	r := Result{Reasons: []Reason{}, Notes: []string{}}
	add := func(st, rule, f string, a ...any) {
		r.Reasons = append(r.Reasons, Reason{Status: st, Rule: rule, Text: fmt.Sprintf(f, a...)})
	}
	secs := func(t time.Time) *float64 { s := in.Now.Sub(t).Seconds(); return &s }
	t := in.Targets

	if in.NewestChecked != nil {
		r.TheoreticalRPO = secs(in.NewestChecked.TakenAt)
	}
	if p := in.LatestPassingTest; p != nil {
		r.ProvenRPO, r.ProofAge, r.ProvenRTO = secs(p.PointTakenAt), secs(p.FinishedAt), p.ProvenRTOS
	}

	// CRITICAL rows.
	if t != nil && r.TheoreticalRPO != nil && *r.TheoreticalRPO > float64(t.RPOS) {
		add(Critical, "theoretical_rpo_over_target", "the newest checked backup (point %d) is %s old, over the RPO target %s",
			in.NewestChecked.ID, dur(*r.TheoreticalRPO), dur(float64(t.RPOS)))
	}
	if lt := in.LatestTest; lt != nil && lt.Verdict == Critical {
		add(Critical, "latest_test_critical", "the latest restore test (%d, point %d) is CRITICAL: %s", lt.ID, lt.PointID, lt.Detail)
	}
	if len(in.DamagedPoints) > 0 {
		add(Critical, "integrity_failed", "integrity check failed for point(s) %v", in.DamagedPoints)
	}

	// UNKNOWN rows: a missing input.
	if t == nil {
		add(Unknown, "no_policy", "the VM has no policy, so it has no targets")
	}
	if in.NewestChecked == nil {
		add(Unknown, "no_checked_point", "there is no backup point that passed its post-backup check")
	}
	if in.LatestPassingTest == nil {
		add(Unknown, "no_passing_test", "no restore test of this VM has ever passed")
	} else if r.ProvenRTO == nil {
		add(Unknown, "no_proven_rto", "restore test %d passed but has no proven RTO", in.LatestPassingTest.ID)
	}

	// WARNING and OK rows need the targets, a theoretical RPO within target, and a complete proof.
	if t != nil && r.TheoreticalRPO != nil && *r.TheoreticalRPO <= float64(t.RPOS) && r.ProvenRPO != nil && r.ProvenRTO != nil {
		p := in.LatestPassingTest
		missed := false
		if *r.ProvenRPO > float64(t.RPOS) {
			missed = true
			add(Warning, "proven_rpo_over_target", "the newest proven point (%d, test %d) is %s old, over the RPO target %s",
				p.PointID, p.ID, dur(*r.ProvenRPO), dur(float64(t.RPOS)))
		}
		if *r.ProvenRTO > float64(t.RTOS) {
			missed = true
			add(Warning, "proven_rto_over_target", "the proven RTO %s (test %d) is over the RTO target %s",
				dur(*r.ProvenRTO), p.ID, dur(float64(t.RTOS)))
		}
		if *r.ProofAge > float64(t.MaxProofAgeS) {
			missed = true
			add(Warning, "proof_too_old", "the proof (test %d) is %s old, over the maximum %s",
				p.ID, dur(*r.ProofAge), dur(float64(t.MaxProofAgeS)))
		}
		if !missed {
			add(OK, "proven", "point %d is proven by test %d: proven RPO %s <= %s, proven RTO %s <= %s, proof age %s <= %s",
				p.PointID, p.ID, dur(*r.ProvenRPO), dur(float64(t.RPOS)), dur(*r.ProvenRTO), dur(float64(t.RTOS)),
				dur(*r.ProofAge), dur(float64(t.MaxProofAgeS)))
		}
	}

	if lt := in.LatestTest; lt != nil && lt.Verdict == Warning {
		r.Notes = append(r.Notes, fmt.Sprintf("the latest restore test (%d, point %d) is WARNING: %s", lt.ID, lt.PointID, lt.Detail))
	}
	if p := in.NewestChecked; p != nil && p.FullReason != "" {
		r.Notes = append(r.Notes, fmt.Sprintf("the newest backup (point %d) is a full backup: %s", p.ID, p.FullReason))
	}
	if a := in.LatestAttempt; a != nil && a.Verdict == Unknown && (in.LatestTest == nil || a.ID > in.LatestTest.ID) {
		r.Notes = append(r.Notes, fmt.Sprintf("restore test %d (point %d) could not run: %s", a.ID, a.PointID, a.Detail))
	}
	if len(r.Reasons) == 0 { // cannot happen with the rows above; never default to healthy
		add(Unknown, "no_rule", "no row of the status table applied")
	}
	slices.SortStableFunc(r.Reasons, func(a, b Reason) int { return cmp.Compare(rank[b.Status], rank[a.Status]) })
	r.Status = r.Reasons[0].Status
	return r
}

// dur renders seconds for a reason: 90 -> "1m30s".
func dur(s float64) string {
	return time.Duration(s * float64(time.Second)).Round(time.Second).String()
}
