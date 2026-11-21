package server

import (
	"bytes"
	"context"
	"fmt"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// Weakening a policy needs a step-up (ADR-008 §4): any target made looser, a test interval made longer. Fewer points
// kept, and other checks, count as looser too: they prove less.

// looser lists what makes policy b looser than a.
func looser(a, b store.Policy) []string {
	var out []string
	more := func(name string, x, y int64) {
		if y > x {
			out = append(out, fmt.Sprintf("%s %d -> %d", name, x, y))
		}
	}
	fewer := func(name string, x, y int) {
		if y < x {
			out = append(out, fmt.Sprintf("%s %d -> %d", name, x, y))
		}
	}
	more("backup_interval_s", a.BackupIntervalS, b.BackupIntervalS)
	more("test_interval_s", a.TestIntervalS, b.TestIntervalS)
	more("rpo_target_s", a.RPOTargetS, b.RPOTargetS)
	more("rto_target_s", a.RTOTargetS, b.RTOTargetS)
	more("max_proof_age_s", a.MaxProofAgeS, b.MaxProofAgeS)
	fewer("keep_last", a.KeepLast, b.KeepLast)
	fewer("keep_daily", a.KeepDaily, b.KeepDaily)
	fewer("keep_weekly", a.KeepWeekly, b.KeepWeekly)
	if a.CheckTemplate != b.CheckTemplate || !bytes.Equal(bytes.TrimSpace(a.Checks), bytes.TrimSpace(b.Checks)) {
		out = append(out, "other checks")
	}
	return out
}

// looserPatch lists what a patch makes looser in a policy.
func looserPatch(cur store.Policy, p store.PolicyPatch) []string {
	n := cur
	set64 := func(dst *int64, v *int64) {
		if v != nil {
			*dst = *v
		}
	}
	set := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	set64(&n.BackupIntervalS, p.BackupIntervalS)
	set64(&n.TestIntervalS, p.TestIntervalS)
	set64(&n.RPOTargetS, p.RPOTargetS)
	set64(&n.RTOTargetS, p.RTOTargetS)
	set64(&n.MaxProofAgeS, p.MaxProofAgeS)
	set(&n.KeepLast, p.KeepLast)
	set(&n.KeepDaily, p.KeepDaily)
	set(&n.KeepWeekly, p.KeepWeekly)
	if p.CheckTemplate != nil {
		n.CheckTemplate = *p.CheckTemplate
	}
	if p.Checks != nil {
		n.Checks = p.Checks
	}
	return looser(cur, n)
}

// looserAssignment lists what giving a VM another policy (0: none) weakens.
func (s *Server) looserAssignment(ctx context.Context, vmID, policyID int64) ([]string, error) {
	vm, err := s.db.VM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if vm.PolicyID == nil || *vm.PolicyID == policyID {
		return nil, nil // a VM without a policy is protected by none: any policy is stronger
	}
	if policyID == 0 {
		return []string{"its policy removed"}, nil
	}
	cur, err := s.db.Policy(ctx, *vm.PolicyID)
	if err != nil {
		return nil, err
	}
	next, err := s.db.Policy(ctx, policyID)
	if err != nil {
		return nil, err
	}
	return looser(cur, next), nil
}
