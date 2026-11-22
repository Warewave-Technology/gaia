package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/capacity"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Proof capacity (ADR-007 C4, internal/capacity): what the sandbox can prove and when, shown, never assumed.

// capacityFacts caches the capacity's inputs for capacityTTL: they are a few thousand rows.
type capacityCache struct {
	mu    sync.Mutex
	at    time.Time
	facts []store.CapacityFact
}

const capacityTTL = 30 * time.Second

func (s *Server) capacityFacts(ctx context.Context) ([]store.CapacityFact, error) {
	s.capCache.mu.Lock()
	defer s.capCache.mu.Unlock()
	if s.capCache.facts != nil && time.Since(s.capCache.at) < capacityTTL {
		return s.capCache.facts, nil
	}
	fs, err := s.db.CapacityFacts(ctx)
	if err != nil {
		return nil, err
	}
	s.capCache.facts, s.capCache.at = fs, time.Now()
	return fs, nil
}

// osClass is a VM's class for the expected durations, and where it came from (ADR-007 C13): first the VM's os: tag
// (os:windows, os:linux), then the guest information libvirt reports, and only last the check template's name. Gaia
// does not record guest information for a protected VM (the libosinfo id is read only for a converted migration
// check, internal/migrate), so that step has nothing to read yet and the order is tag, then template.
func osClass(tag, template string) (string, string) {
	switch t := strings.ToLower(strings.TrimSpace(tag)); {
	case strings.HasPrefix(t, "windows"):
		return "windows", "tag os:" + tag
	case t == "linux":
		return "linux", "tag os:" + tag
	}
	basis := "check template " + template
	if tag != "" {
		basis = "check template " + template + " (tag os:" + tag + " is neither windows nor linux)"
	}
	if strings.Contains(template, "windows") {
		return "windows", basis
	}
	return "linux", basis
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	return &t
}

// PolicyChange is a what-if change of one policy. The RPO target changes only how much of the time the policy's VMs
// can be OK (policies in the answer), not the capacity.
type PolicyChange struct {
	PolicyID      int64  `json:"policy_id"`
	TestIntervalS *int64 `json:"test_interval_s,omitempty"`
	MaxProofAgeS  *int64 `json:"max_proof_age_s,omitempty"`
	RPOTargetS    *int64 `json:"rpo_target_s,omitempty"`
}

// PolicyOK is how much of the time a VM on a policy can be OK at best, and the test interval that would make it
// always possible (ADR-007 C12, status.OKBound); null when nothing needs to change.
type PolicyOK struct {
	PolicyID               int64   `json:"policy_id"`
	MaxOKFraction          float64 `json:"max_ok_fraction"`
	SuggestedTestIntervalS *int64  `json:"suggested_test_interval_s"`
}

// PolicyView is a policy as the API shows it: with how much of the time its VMs can be OK (C12). Saving a policy
// that cannot always be OK is allowed.
type PolicyView struct {
	store.Policy
	MaxOKFraction          float64 `json:"max_ok_fraction"`           // 0..1; 1 when always possible
	SuggestedTestIntervalS *int64  `json:"suggested_test_interval_s"` // null when nothing needs to change
}

func policyOK(p store.Policy) (float64, *int64) {
	return status.OKBound(status.Targets{RPOS: p.RPOTargetS, RTOS: p.RTOTargetS, MaxProofAgeS: p.MaxProofAgeS}, p.TestIntervalS)
}

func policyView(p store.Policy) PolicyView {
	v := PolicyView{Policy: p}
	v.MaxOKFraction, v.SuggestedTestIntervalS = policyOK(p)
	return v
}

func policyViewOf(p store.Policy, err error) (PolicyView, error) { return policyView(p), err }

func policyViews(ps []store.Policy, err error) ([]PolicyView, error) {
	out := make([]PolicyView, 0, len(ps))
	for _, p := range ps {
		out = append(out, policyView(p))
	}
	return out, err
}

// whatIfPolicies is the C12 figure of every changed policy, with the change applied; nothing is saved.
func (s *Server) whatIfPolicies(ctx context.Context, w WhatIf) ([]PolicyOK, error) {
	out := []PolicyOK{}
	for _, c := range w.Policies {
		p, err := s.db.Policy(ctx, c.PolicyID)
		if err != nil {
			return nil, err
		}
		if c.TestIntervalS != nil {
			p.TestIntervalS = *c.TestIntervalS
		}
		if c.MaxProofAgeS != nil {
			p.MaxProofAgeS = *c.MaxProofAgeS
		}
		if c.RPOTargetS != nil {
			p.RPOTargetS = *c.RPOTargetS
		}
		x := PolicyOK{PolicyID: p.ID}
		x.MaxOKFraction, x.SuggestedTestIntervalS = policyOK(p)
		out = append(out, x)
	}
	return out, nil
}

// WhatIf is a changed slot count or changed policies; nothing is saved.
type WhatIf struct {
	Slots    *int           `json:"slots,omitempty"`
	Policies []PolicyChange `json:"policies,omitempty"`
}

// capacityInput is the capacity's input now, with a what-if applied.
func (s *Server) capacityInput(ctx context.Context, w WhatIf) (capacity.Input, error) {
	fs, err := s.capacityFacts(ctx)
	if err != nil {
		return capacity.Input{}, err
	}
	changes := map[int64]PolicyChange{}
	for _, p := range w.Policies {
		if p.TestIntervalS != nil && *p.TestIntervalS < 60 || p.MaxProofAgeS != nil && *p.MaxProofAgeS <= 0 || p.RPOTargetS != nil && *p.RPOTargetS <= 0 {
			return capacity.Input{}, fmt.Errorf("%w: policy %d: test_interval_s >= 60, max_proof_age_s > 0, rpo_target_s > 0", store.ErrInvalid, p.PolicyID)
		}
		changes[p.PolicyID] = p
	}
	in := capacity.Input{Now: s.now().UTC(), Slots: s.testSlotsNow()}
	if w.Slots != nil {
		if *w.Slots < 0 || *w.Slots > 64 {
			return in, fmt.Errorf("%w: slots 0-64", store.ErrInvalid)
		}
		in.Slots = *w.Slots
	}
	for _, f := range fs {
		v := capacity.VM{ID: f.VMID, Name: f.VM, PolicyID: f.PolicyID, Policy: f.Policy, TestIntervalS: f.TestIntervalS,
			MaxProofAgeS: f.MaxProofAgeS, Durations: f.Durations, Critical: f.Rank == store.RankCritical, LastQueued: parseTime(f.LastQueued),
			QueuedJob: f.QueuedJob, QueuedAt: parseTime(f.QueuedAt), RunningSince: parseTime(f.RunningSince)}
		v.Class, v.ClassBasis = osClass(f.OSTag, f.CheckTemplate)
		if f.ProofAt != nil {
			t := timeOf(*f.ProofAt)
			v.ProofAt = &t
		}
		if c, ok := changes[f.PolicyID]; ok {
			if c.TestIntervalS != nil {
				v.TestIntervalS = *c.TestIntervalS
			}
			if c.MaxProofAgeS != nil {
				v.MaxProofAgeS = *c.MaxProofAgeS
			}
		}
		in.VMs = append(in.VMs, v)
	}
	return in, nil
}

// testSlotsNow is the slots the capacity counts: the helper's, 0 while it does not answer (nothing is proven then).
func (s *Server) testSlotsNow() int {
	c, err := s.sandboxCapacity()
	if err != nil || c == nil {
		return 0
	}
	return c.Slots
}

// CapacityView is the proof capacity: the numbers, the groups, and the first entries of the lists.
type CapacityView struct {
	Clock ServerClock `json:"clock"`
	capacity.Result
	ExpiringTotal int               `json:"expiring_total"`
	QueueTotal    int               `json:"queue_total"`
	Sandbox       *sandbox.Capacity `json:"sandbox"`       // the helper now; nil when it does not answer
	SandboxError  string            `json:"sandbox_error"` // why
	WhatIf        *WhatIf           `json:"what_if,omitempty"`
	// What-if only: each changed policy's C12 figure with the change applied, for the policy editor before it saves.
	Policies []PolicyOK `json:"policies,omitempty"`
}

func (s *Server) capacityResult(ctx context.Context, w WhatIf, limit int) (CapacityView, error) {
	s.refreshSandbox(ctx)
	in, err := s.capacityInput(ctx, w)
	if err != nil {
		return CapacityView{}, err
	}
	r := capacity.Compute(in)
	out := CapacityView{Clock: serverClock(s.now()), Result: r, ExpiringTotal: len(r.Expiring), QueueTotal: len(r.Queue)}
	out.Expiring, out.Queue = r.Expiring[:min(limit, len(r.Expiring))], r.Queue[:min(limit, len(r.Queue))]
	if c, err := s.sandboxCapacity(); err != nil {
		out.SandboxError = err.Error()
	} else {
		out.Sandbox = c
	}
	return out, nil
}

func (s *Server) capacityView(c *call) (int, any, error) {
	n, err := c.intQuery("limit", 50, 1000)
	if err != nil {
		return 0, nil, err
	}
	return one(s.capacityResult(c.r.Context(), WhatIf{}, int(n)))
}

func (s *Server) whatIfView(c *call) (int, any, error) {
	if err := s.limitWhatIf(c); err != nil {
		return 0, nil, err
	}
	var w WhatIf
	if err := c.decode(&w); err != nil {
		return 0, nil, err
	}
	v, err := s.capacityResult(c.r.Context(), w, 50)
	if err != nil {
		return 0, nil, err
	}
	v.WhatIf = &w
	if len(w.Policies) > 0 {
		if v.Policies, err = s.whatIfPolicies(c.r.Context(), w); err != nil {
			return 0, nil, err
		}
	}
	return http.StatusOK, v, nil
}

// TestQueueView is the queued restore tests in the order they start, with their estimated starts, in pages; and the
// sandbox's running tests.
type TestQueueView struct {
	Clock   ServerClock          `json:"clock"`
	Slots   int                  `json:"slots"`
	Total   int                  `json:"total"`
	Items   []capacity.QueueItem `json:"items"`
	Next    string               `json:"next"`
	Running []sandbox.Running    `json:"running"`
	Waiting []sandbox.Waiting    `json:"waiting_for_admission"`
}

func (s *Server) testQueueView(c *call) (int, any, error) {
	off, n, err := offsetPage(c, 50)
	if err != nil {
		return 0, nil, err
	}
	v, err := s.capacityResult(c.r.Context(), WhatIf{}, 1<<30)
	if err != nil {
		return 0, nil, err
	}
	out := TestQueueView{Clock: v.Clock, Slots: v.Slots, Total: len(v.Queue), Items: []capacity.QueueItem{}, Running: []sandbox.Running{},
		Waiting: []sandbox.Waiting{}}
	for i := off; i < len(v.Queue) && i < off+n; i++ {
		out.Items = append(out.Items, v.Queue[i])
	}
	if off+n < len(v.Queue) {
		out.Next = fmt.Sprint(off + n)
	}
	if v.Sandbox != nil {
		out.Running, out.Waiting = v.Sandbox.Running, v.Sandbox.Waiting
	}
	return http.StatusOK, out, nil
}

// CapacityInput is one VM's line of the capacity's inputs, for checking the numbers by hand.
type CapacityInput struct {
	VMID          int64     `json:"vm_id"`
	VM            string    `json:"vm"`
	Policy        string    `json:"policy"`
	OSClass       string    `json:"os_class"`
	OSClassBasis  string    `json:"os_class_basis"` // where the class came from: the os: tag, or the check template (ADR-007 C13)
	TestIntervalS int64     `json:"test_interval_s"`
	MaxProofAgeS  int64     `json:"max_proof_age_s"`
	Durations     []float64 `json:"durations_s"` // its last tests that ran, newest first (at most 10)
	ExpectedS     float64   `json:"expected_s"`
	Basis         string    `json:"basis"`
}

// CapacityInputs is a page of the inputs.
type CapacityInputs struct {
	Total        int                `json:"total"`
	ClassMedians map[string]float64 `json:"class_medians_s"`
	Items        []CapacityInput    `json:"items"`
	Next         string             `json:"next"`
}

func (s *Server) capacityInputs(c *call) (int, any, error) {
	off, n, err := offsetPage(c, 200)
	if err != nil {
		return 0, nil, err
	}
	in, err := s.capacityInput(c.r.Context(), WhatIf{})
	if err != nil {
		return 0, nil, err
	}
	cm := capacity.ClassMedians(in.VMs)
	out := CapacityInputs{Total: len(in.VMs), ClassMedians: cm, Items: []CapacityInput{}}
	for i := off; i < len(in.VMs) && i < off+n; i++ {
		v := in.VMs[i]
		e, b := capacity.Expected(v, cm)
		d := v.Durations[:min(10, len(v.Durations))]
		if d == nil {
			d = []float64{}
		}
		out.Items = append(out.Items, CapacityInput{VMID: v.ID, VM: v.Name, Policy: v.Policy, OSClass: v.Class, OSClassBasis: v.ClassBasis, TestIntervalS: v.TestIntervalS,
			MaxProofAgeS: v.MaxProofAgeS, Durations: d, ExpectedS: e, Basis: b})
	}
	if off+n < len(in.VMs) {
		out.Next = fmt.Sprint(off + n)
	}
	return http.StatusOK, out, nil
}
