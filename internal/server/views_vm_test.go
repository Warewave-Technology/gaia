package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
)

// The VM page's points and status history come in pages, newest first, and every page says what proves its points.
func TestVMPagedLists(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	db, vmID, tok := f.s.db, f.ids["vms"], f.tokens[store.RoleAuditor]
	seedPoint, err := db.Point(ctx, f.ids["points"])
	if err != nil {
		t.Fatal(err)
	}
	mk := func(at string) store.Point {
		j, err := db.CreateJob(ctx, "system", "backup", &vmID, "schedule")
		if err != nil {
			t.Fatal(err)
		}
		p, err := db.CreatePoint(ctx, "system", store.Point{VMID: vmID, JobID: j.ID, RepositoryID: seedPoint.RepositoryID, Kind: "incremental", TakenAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	finish := func(p store.Point, verdict string, rto *float64, detail any) store.RestoreTest {
		rt, err := db.CreateRestoreTest(ctx, "system", store.RestoreTest{VMID: vmID, PointID: p.ID, JobID: p.JobID})
		if err != nil {
			t.Fatal(err)
		}
		if detail == nil {
			return rt
		}
		if rt, err = db.FinishRestoreTest(ctx, "system", rt.ID, verdict, rto, "test", "", detail); err != nil {
			t.Fatal(err)
		}
		return rt
	}
	rto := 52.0
	pruned := mk("2026-11-16T13:00:00Z")
	if err := db.PrunePoint(ctx, "system", pruned.ID, "retention"); err != nil {
		t.Fatal(err)
	}
	proven, failed, running, damaged := mk("2026-11-16T14:00:00Z"), mk("2026-11-16T15:00:00Z"), mk("2026-11-16T16:00:00Z"), mk("2026-11-16T17:00:00Z")
	pass := finish(proven, verify.OK, &rto, TestDetail{Checks: []verify.CheckResult{{Name: "boot", Verdict: verify.OK}}})
	finish(proven, verify.Critical, nil, TestDetail{Checks: []verify.CheckResult{{Name: "boot", Verdict: verify.OK}, {Name: "stable", Verdict: verify.Critical}}})
	fail := finish(failed, verify.Critical, nil, TestDetail{Checks: []verify.CheckResult{{Name: "boot", Verdict: verify.Critical}}})
	down := finish(running, verify.Unknown, nil, TestDetail{SandboxDown: true})
	now := finish(running, "", nil, nil)
	if _, err := db.SetPointCheck(ctx, "system", damaged.ID, false, "chunk missing"); err != nil {
		t.Fatal(err)
	}

	// Five kept points (the seed's and four), one pruned; pages of two, newest first.
	var got []VMPoint
	before := ""
	for page := 0; ; page++ {
		var v VMPointsView
		q := "?limit=2"
		if before != "" {
			q += "&before=" + before
		}
		if st := f.do(tok, "GET", fmt.Sprintf("/api/v1/views/vms/%d/points%s", vmID, q), "", &v); st != 200 {
			t.Fatalf("points page %d: %d", page, st)
		}
		if v.Total != 5 || v.Pruned != 1 || len(v.Rows) > 2 {
			t.Fatalf("points page %d: total %d, pruned %d, %d rows", page, v.Total, v.Pruned, len(v.Rows))
		}
		got = append(got, v.Rows...)
		if v.Next == "" {
			break
		}
		before = v.Next
		if page > 5 {
			t.Fatal("points: the pages do not end")
		}
	}
	want := []int64{damaged.ID, running.ID, failed.ID, proven.ID, seedPoint.ID}
	if len(got) != len(want) {
		t.Fatalf("points: %d rows, want %d", len(got), len(want))
	}
	by := map[int64]VMPoint{}
	for i, r := range got {
		if r.ID != want[i] {
			t.Fatalf("points order: row %d is %d, want %d", i, r.ID, want[i])
		}
		by[r.ID] = r
	}
	if r := by[proven.ID]; r.Proven == nil || r.Proven.ID != pass.ID || r.Proven.ProvenRTOS == nil || *r.Proven.ProvenRTOS != rto ||
		r.Latest == nil || r.Latest.Verdict != verify.Critical || r.Latest.FailedCheck != "stable" {
		t.Fatalf("a point proven, then failed: %+v %+v", r.Proven, r.Latest)
	}
	if r := by[failed.ID]; r.Proven != nil || r.Latest == nil || r.Latest.ID != fail.ID || r.Latest.FailedCheck != "boot" {
		t.Fatalf("a point whose test failed to boot: %+v %+v", r.Proven, r.Latest)
	}
	if r := by[running.ID]; r.Proven != nil || r.Latest == nil || r.Latest.ID != down.ID || !r.Latest.SandboxDown || r.RunningTestID != now.ID {
		t.Fatalf("a point under test after one that could not run: %+v %+v %d", r.Proven, r.Latest, r.RunningTestID)
	}
	if r := by[damaged.ID]; r.CheckState != "failed" || r.Latest != nil {
		t.Fatalf("a damaged point: %+v", r)
	}
	if st := f.do(tok, "GET", fmt.Sprintf("/api/v1/views/vms/%d/points?limit=101", vmID), "", nil); st != 400 {
		t.Fatalf("points limit 101: %d", st)
	}
	if st := f.do(tok, "GET", "/api/v1/views/vms/999999/points", "", nil); st != 404 {
		t.Fatalf("points of no VM: %d", st)
	}

	// Status history: three more changes, pages of two, newest first; each says until when it lasted.
	for i, to := range []string{"CRITICAL", "OK", "WARNING"} {
		if _, err := db.SQL().ExecContext(ctx, `INSERT INTO state_changes (at, entity, entity_id, from_state, to_state, reason, actor, code) VALUES (?, 'vm_status', ?, '', ?, 'test', 'system', 'proven')`,
			fmt.Sprintf("2026-11-17T1%d:00:00Z", i), vmID, to); err != nil {
			t.Fatal(err)
		}
	}
	all, err := db.StateChanges(ctx, "vm_status", vmID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var hist []VMStatusChange
	before = ""
	for page := 0; ; page++ {
		var v VMStatusHistoryView
		q := "?limit=2"
		if before != "" {
			q += "&before=" + before
		}
		if st := f.do(tok, "GET", fmt.Sprintf("/api/v1/views/vms/%d/status-history%s", vmID, q), "", &v); st != 200 || v.Total != len(all) || len(v.Rows) > 2 {
			t.Fatalf("status history page %d: %d, total %d of %d, %d rows", page, st, v.Total, len(all), len(v.Rows))
		}
		hist = append(hist, v.Rows...)
		if v.Next == "" {
			break
		}
		before = v.Next
		if page > len(all) {
			t.Fatal("status history: the pages do not end")
		}
	}
	if len(hist) != len(all) || len(all) < 4 {
		t.Fatalf("status history: %d rows of %d", len(hist), len(all))
	}
	for i, r := range hist {
		old := all[len(all)-1-i]
		until := ""
		if i > 0 {
			until = hist[i-1].At
		}
		if r.Seq != old.Seq || r.Until != until {
			t.Fatalf("status history row %d: seq %d until %q, want seq %d until %q", i, r.Seq, r.Until, old.Seq, until)
		}
	}
}
