package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
)

// The VM page's two long lists, paged on the server (T27 step 1b; ADR-007 C1, C15; docs/design/v3 rule 1): its kept
// points with what proves each one, and its status changes, both newest first. GET /views/vms/{id} keeps its own
// points and status_history for the screens of M2; these are what the v3 tabs read.

// PointTest is what one restore test says about the point it restored.
type PointTest struct {
	ID          int64    `json:"id"`
	Verdict     string   `json:"verdict"`
	FinishedAt  string   `json:"finished_at"`
	ProvenRTOS  *float64 `json:"proven_rto_s"`
	FailedCheck string   `json:"failed_check"` // the first CRITICAL check (boot, stable, ...), for a CRITICAL test
	SandboxDown bool     `json:"sandbox_down"` // it could not run: the sandbox helper did not answer
}

// VMPoint is one kept point and its proof.
type VMPoint struct {
	ID         int64  `json:"id"`
	JobID      int64  `json:"job_id"`
	Kind       string `json:"kind"`
	TakenAt    string `json:"taken_at"`
	Bytes      int64  `json:"bytes"`
	CheckState string `json:"check_state"` // unchecked, ok, failed
	KindReason string `json:"kind_reason"` // why the point is a full or an incremental one
	// Proven is its newest passing restore test; Latest its newest finished one, whatever the verdict.
	Proven *PointTest `json:"proven"`
	Latest *PointTest `json:"latest"`
	// RunningTestID is a test restoring it now (0: none).
	RunningTestID int64 `json:"running_test_id"`
}

// VMPointsView is one page of a VM's kept points, newest first.
type VMPointsView struct {
	Total  int       `json:"total"`  // kept (present) points
	Pruned int       `json:"pruned"` // points retention pruned or an admin forgot
	Rows   []VMPoint `json:"rows"`
	Next   string    `json:"next"` // before= for the next page; empty on the last
}

func (s *Server) vmPointsView(c *call) (int, any, error) {
	ctx := c.r.Context()
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	if _, err := s.db.VM(ctx, id); err != nil {
		return 0, nil, err
	}
	before, err := c.intQuery("before", 0, 0)
	if err != nil {
		return 0, nil, err
	}
	n, err := c.intQuery("limit", 25, 100)
	if err != nil {
		return 0, nil, err
	}
	if n == 0 {
		n = 25
	}
	out := VMPointsView{Rows: []VMPoint{}}
	if out.Total, out.Pruned, err = s.db.PointCounts(ctx, id); err != nil {
		return 0, nil, err
	}
	pts, err := s.db.KeptPoints(ctx, id, before, int(n)+1)
	if err != nil {
		return 0, nil, err
	}
	if len(pts) > int(n) {
		pts = pts[:n]
		out.Next = strconv.FormatInt(pts[len(pts)-1].ID, 10)
	}
	ids := make([]int64, len(pts))
	for i, p := range pts {
		ids[i] = p.ID
	}
	tests, err := s.db.TestsOfPoints(ctx, ids)
	if err != nil {
		return 0, nil, err
	}
	for _, p := range pts {
		row := VMPoint{ID: p.ID, JobID: p.JobID, Kind: p.Kind, TakenAt: p.TakenAt, Bytes: p.Bytes, CheckState: p.CheckState, KindReason: p.Reason}
		for _, t := range tests { // newest first
			if t.PointID != p.ID {
				continue
			}
			if t.FinishedAt == "" {
				if row.RunningTestID == 0 {
					row.RunningTestID = t.ID
				}
				continue
			}
			pt, err := pointTest(t)
			if err != nil {
				return 0, nil, err
			}
			if row.Latest == nil {
				row.Latest = &pt
			}
			if row.Proven == nil && t.Verdict == verify.OK {
				row.Proven = &pt
			}
		}
		out.Rows = append(out.Rows, row)
	}
	return http.StatusOK, out, nil
}

// pointTest reads a finished test's failed check and whether the sandbox ran it from its detail.
func pointTest(t store.RestoreTest) (PointTest, error) {
	pt := PointTest{ID: t.ID, Verdict: t.Verdict, FinishedAt: t.FinishedAt, ProvenRTOS: t.ProvenRTOS}
	if len(t.Detail) == 0 {
		return pt, nil
	}
	var d TestDetail
	if err := json.Unmarshal(t.Detail, &d); err != nil {
		return pt, fmt.Errorf("restore test %d: detail: %w", t.ID, err)
	}
	pt.SandboxDown = d.SandboxDown
	if i := slices.IndexFunc(d.Checks, func(c verify.CheckResult) bool { return c.Verdict == verify.Critical }); i >= 0 {
		pt.FailedCheck = d.Checks[i].Name
	}
	return pt, nil
}

// VMStatusChange is one change of a VM's status and how long the status it set lasted.
type VMStatusChange struct {
	store.StateChange
	Until string `json:"until"` // when the next change came; "" for the status the VM has now
}

// VMStatusHistoryView is one page of a VM's status changes, newest first.
type VMStatusHistoryView struct {
	Total int              `json:"total"`
	Rows  []VMStatusChange `json:"rows"`
	Next  string           `json:"next"` // before= for the next page; empty on the last
}

func (s *Server) vmStatusHistoryView(c *call) (int, any, error) {
	ctx := c.r.Context()
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	if _, err := s.db.VM(ctx, id); err != nil {
		return 0, nil, err
	}
	before, err := c.intQuery("before", 0, 0)
	if err != nil {
		return 0, nil, err
	}
	n, err := c.intQuery("limit", 25, 100)
	if err != nil {
		return 0, nil, err
	}
	if n == 0 {
		n = 25
	}
	out := VMStatusHistoryView{Rows: []VMStatusChange{}}
	if out.Total, err = s.db.CountEntityChanges(ctx, "vm_status", id); err != nil {
		return 0, nil, err
	}
	rows, err := s.db.StateChangesBefore(ctx, "vm_status", id, before, int(n)+1)
	if err != nil {
		return 0, nil, err
	}
	if len(rows) > int(n) {
		rows = rows[:n]
		out.Next = strconv.FormatInt(rows[len(rows)-1].Seq, 10)
	}
	until := ""
	if len(rows) > 0 && before > 0 {
		// the newest row of this page lasted until the oldest row of the page before it
		if until, err = s.db.StateChangeAfter(ctx, "vm_status", id, rows[0].Seq); err != nil {
			return 0, nil, err
		}
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, VMStatusChange{StateChange: r, Until: until})
		until = r.At
	}
	return http.StatusOK, out, nil
}
