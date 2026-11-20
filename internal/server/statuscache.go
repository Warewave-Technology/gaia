package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// The status table (T24, ADR-007 C1): vm_status is the status model's cache. Every event that can change a VM's
// status recomputes that VM (refreshStatus); time alone changes it only at the moment status.NextChange names, so the
// scheduler recomputes the VMs that are due at every tick, and every VM once an hour. Reads never recompute: they
// render the stored reasons and figures for the time of the read. GET /api/v1/status/check compares the table with
// the status model over the whole catalog.

// fullRecomputeEvery is how often every VM is recomputed, whatever is due: a safety net, never the mechanism.
const fullRecomputeEvery = time.Hour

// recomputeBatch is how many VMs a recompute writes in one transaction.
const recomputeBatch = 250

func unixOf(t time.Time) *float64 {
	u := float64(t.UnixNano()) / 1e9
	return &u
}

func timeOf(u float64) time.Time { return time.Unix(0, int64(u*1e9)).UTC() }

// statusRow is what the table keeps of a computation besides its result.
func statusRow(in status.Input, r status.Result) store.StatusRow {
	row := store.StatusRow{Rank: statusRank[r.Status], Code: string(r.Reasons[0].Code)}
	if sc := r.Reasons[0].Scope; sc != nil {
		row.ScopeKind, row.ScopeID, row.ScopeName = sc.Kind, sc.ID, sc.Name
	}
	if p := in.NewestChecked; p != nil {
		id := p.ID
		row.NewestPointID, row.NewestTakenAt = &id, unixOf(p.TakenAt)
	}
	if p := in.LatestPassingTest; p != nil {
		pid, tid := p.PointID, p.ID
		row.ProvenPointID, row.ProvenTakenAt, row.ProvenTestID, row.ProvenFinishedAt = &pid, unixOf(p.PointTakenAt), &tid, unixOf(p.FinishedAt)
		row.ProvenRTOS = p.ProvenRTOS
	}
	if t := in.Targets; t != nil {
		rpo, rto, age := t.RPOS, t.RTOS, t.MaxProofAgeS
		row.RPOTargetS, row.RTOTargetS, row.MaxProofAgeS = &rpo, &rto, &age
	}
	if n := status.NextChange(in); n != nil {
		row.NextChangeAt = unixOf(*n)
	}
	return row
}

// renderStatus is a stored status as it reads at now: the sentences written for now, the ages grown with the clock.
func renderStatus(row store.VMStatus, now time.Time) (status.Result, error) {
	var r status.Result
	if err := json.Unmarshal(row.Result, &r); err != nil {
		return r, fmt.Errorf("VM %d: stored status: %w", row.VMID, err)
	}
	for i := range r.Reasons {
		r.Reasons[i].Text = status.Render(r.Reasons[i], now)
	}
	for i := range r.NoteReasons {
		r.NoteReasons[i].Text = status.Render(r.NoteReasons[i], now)
		if i < len(r.Notes) {
			r.Notes[i] = r.NoteReasons[i].Text
		}
	}
	age := func(u *float64) *float64 {
		if u == nil {
			return nil
		}
		s := now.Sub(timeOf(*u)).Seconds()
		return &s
	}
	r.TheoreticalRPO, r.ProvenRPO, r.ProofAge, r.ProvenRTO = age(row.NewestTakenAt), age(row.ProvenTakenAt), age(row.ProvenFinishedAt), row.ProvenRTOS
	return r, nil
}

// storedStatus is a VM's stored status rendered for now; a VM never computed is computed first.
func (s *Server) storedStatus(ctx context.Context, vmID int64) (status.Result, store.VMStatus, error) {
	rows, err := s.db.VMStatuses(ctx, vmID)
	if err != nil {
		return status.Result{}, store.VMStatus{}, err
	}
	if len(rows) == 0 {
		if _, err := s.refreshStatus(ctx, vmID); err != nil {
			return status.Result{}, store.VMStatus{}, err
		}
		if rows, err = s.db.VMStatuses(ctx, vmID); err != nil || len(rows) == 0 {
			return status.Result{}, store.VMStatus{}, errors.Join(err, fmt.Errorf("VM %d: no status stored", vmID))
		}
	}
	r, err := renderStatus(rows[0], s.now().UTC())
	return r, rows[0], err
}

// RecomputeStats is the newest full recompute of the status table.
type RecomputeStats struct {
	At      string  `json:"at"`
	Reason  string  `json:"reason"` // start, hourly, requested by <user>
	VMs     int     `json:"vms"`
	Changed int     `json:"changed"` // VMs whose status changed
	Seconds float64 `json:"seconds"`
	Errors  int     `json:"errors"`
}

type recomputeState struct {
	mu       sync.Mutex
	last     *RecomputeStats
	lastFull time.Time
}

// recompute recomputes the given VMs (every VM when ids is nil) and says how it went.
func (s *Server) recompute(ctx context.Context, ids []int64, reason string) (RecomputeStats, error) {
	t0 := time.Now()
	st := RecomputeStats{At: s.now().UTC().Format(time.RFC3339Nano), Reason: reason}
	if ids == nil {
		vms, err := s.db.VMs(ctx, 0)
		if err != nil {
			return st, err
		}
		for _, vm := range vms {
			ids = append(ids, vm.ID)
		}
	}
	// In batches: each batch is computed, then written in one transaction (one commit, not one per VM).
	var errs []error
	note := func(err error) {
		st.Errors++
		if len(errs) < 10 {
			errs = append(errs, err)
		}
	}
	for start := 0; start < len(ids); start += recomputeBatch {
		batch := ids[start:min(start+recomputeBatch, len(ids))]
		ws := make([]store.StatusWrite, 0, len(batch))
		for _, id := range batch {
			in, err := s.statusInput(ctx, id)
			if err != nil {
				note(fmt.Errorf("VM %d: %w", id, err))
				continue
			}
			r := status.Compute(in)
			ws = append(ws, store.StatusWrite{VMID: id, Status: r.Status, Reason: r.Reasons[0].Text, Result: r, Row: statusRow(in, r)})
		}
		changed, err := s.db.SetVMStatuses(ctx, ws)
		if err != nil {
			note(err)
			continue
		}
		for i, c := range changed {
			if c {
				st.Changed++
				s.log.Info("VM status changed", "vm", ws[i].VMID, "status", ws[i].Status, "code", ws[i].Row.Code, "reason", ws[i].Reason)
			}
		}
	}
	st.VMs, st.Seconds = len(ids), time.Since(t0).Seconds()
	return st, errors.Join(errs...)
}

// refreshStatuses is the scheduler's part: the VMs that are due now, and every VM once an hour (and at the start).
func (s *Server) refreshStatuses(ctx context.Context, now time.Time) error {
	s.recomputed.mu.Lock()
	full := s.recomputed.lastFull.IsZero() || now.Sub(s.recomputed.lastFull) >= fullRecomputeEvery
	reason := "hourly"
	if s.recomputed.lastFull.IsZero() {
		reason = "start"
	}
	s.recomputed.mu.Unlock()
	if full {
		st, err := s.recompute(ctx, nil, reason)
		s.recomputed.mu.Lock()
		s.recomputed.last, s.recomputed.lastFull = &st, now
		s.recomputed.mu.Unlock()
		s.log.Info("status table recomputed", "reason", reason, "vms", st.VMs, "changed", st.Changed, "seconds", st.Seconds, "errors", st.Errors)
		return err
	}
	due, err := s.db.DueStatuses(ctx, *unixOf(s.now().UTC()))
	if err != nil || len(due) == 0 {
		return err
	}
	_, err = s.recompute(ctx, due, "due")
	return err
}

// StatusCheck compares the status table with the status model for every VM.
type StatusCheck struct {
	At      string         `json:"at"`
	VMs     int            `json:"vms"`
	Equal   int            `json:"equal"`
	Due     int            `json:"due"`   // the clock passed their next change since the last tick: recomputed at the next one
	Moved   int            `json:"moved"` // changed by an event while the check ran, equal when looked at again
	Differ  []StatusDiffer `json:"differ"`
	Seconds float64        `json:"seconds"`
	Verdict string         `json:"verdict"` // OK when nothing differs
}

// StatusDiffer is a VM whose stored status is not what the model says now.
type StatusDiffer struct {
	VMID   int64  `json:"vm_id"`
	Stored string `json:"stored"` // status and codes
	Model  string `json:"model"`
}

// checkStatuses computes every VM's status without storing it and compares it with the table: the status and the
// code, scope and parameters of every reason and note. Each VM is read and computed together; one that differs is
// read and computed once more, so an event that lands while the check runs (a test finishing) is not a difference.
func (s *Server) checkStatuses(ctx context.Context) (StatusCheck, error) {
	t0 := time.Now()
	now := s.now().UTC()
	out := StatusCheck{At: now.Format(time.RFC3339Nano), Differ: []StatusDiffer{}}
	vms, err := s.db.VMs(ctx, 0)
	if err != nil {
		return out, err
	}
	sig := func(r status.Result) string {
		b, _ := json.Marshal(struct {
			S string
			R []status.Reason
			N []status.Reason
		}{r.Status, stripText(r.Reasons), stripText(r.NoteReasons)})
		return string(b)
	}
	// compare reads the VM's row and computes the model: "equal", "due", or the two signatures.
	compare := func(vmID int64) (string, string, string, error) {
		rows, err := s.db.VMStatuses(ctx, vmID)
		if err != nil {
			return "", "", "", err
		}
		in, err := s.statusInput(ctx, vmID)
		if err != nil {
			return "", "", "", err
		}
		in.Now = now
		model := status.Compute(in)
		if len(rows) == 0 {
			return "differ", "(none)", sig(model), nil
		}
		row := rows[0]
		if row.NextChangeAt != nil && *row.NextChangeAt <= *unixOf(now) {
			return "due", "", "", nil
		}
		var got status.Result
		if err := json.Unmarshal(row.Result, &got); err != nil {
			return "", "", "", fmt.Errorf("stored status: %w", err)
		}
		if a, b := sig(got), sig(model); a != b || row.Status != model.Status {
			return "differ", a, b, nil
		}
		return "equal", "", "", nil
	}
	for _, vm := range vms {
		out.VMs++
		verdict, a, b, err := compare(vm.ID)
		if err == nil && verdict == "differ" {
			if verdict, a, b, err = compare(vm.ID); err == nil && verdict == "equal" {
				verdict = "moved"
			}
		}
		if err != nil {
			return out, fmt.Errorf("VM %s: %w", vm.Name, err)
		}
		switch verdict {
		case "equal":
			out.Equal++
		case "due":
			out.Due++
		case "moved":
			out.Moved++
		default:
			out.Differ = append(out.Differ, StatusDiffer{VMID: vm.ID, Stored: a, Model: b})
		}
	}
	out.Seconds = time.Since(t0).Seconds()
	out.Verdict = status.OK
	if len(out.Differ) > 0 {
		out.Verdict = status.Critical
	}
	return out, nil
}

// stripText drops what is written for a moment (the sentence) and keeps what a reason is.
func stripText(rs []status.Reason) []status.Reason {
	out := make([]status.Reason, len(rs))
	for i, r := range rs {
		r.Text = ""
		out[i] = r
	}
	return out
}

// statusPage is a page of stored statuses, rendered for now.
func (s *Server) statusPage(ctx context.Context, after int64, n int) ([]VMStatus, error) {
	rows, err := s.db.StatusPage(ctx, after, n)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	out := []VMStatus{}
	for _, row := range rows {
		vm, err := s.db.VM(ctx, row.VMID)
		if err != nil {
			return nil, err
		}
		r, err := renderStatus(row, now)
		if err != nil {
			return nil, err
		}
		out = append(out, VMStatus{VMID: vm.ID, VM: vm.Name, ChangedAt: row.ChangedAt, Result: r})
	}
	return out, nil
}
