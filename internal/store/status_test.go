package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

func TestStatusFactsAndVMStatus(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	h, _ := d.CreateHost(ctx, HostInput{Name: "kvm1", Address: "kvm1.lab", SSHUser: "gaia-backup"})
	r, _ := d.CreateRepository(ctx, Repository{Name: "main", Kind: "filesystem", Location: "/r", Splitter: "FIXED-1M", Hash: "BLAKE2B-256-128"})
	v, _ := d.CreateVM(ctx, "admin", VMInput{HostID: h.ID, Name: "web1"})

	f, err := d.StatusFacts(ctx, v.ID)
	if err != nil || f.Policy != nil || f.NewestChecked != nil || f.LatestTest != nil || f.Passing != nil || len(f.Damaged) != 0 {
		t.Fatalf("a new VM: %+v %v", f, err)
	}
	point := func(taken string, check bool) Point {
		j, _ := d.CreateJob(ctx, "system", "backup", &v.ID, "schedule")
		p, err := d.CreateBackupPoint(ctx, "system", Point{VMID: v.ID, JobID: j.ID, RepositoryID: r.ID, Kind: "full", TakenAt: taken,
			Machine: json.RawMessage(`{"mac":"52:54:00:00:00:01"}`)},
			[]PointDisk{{Target: "vda", SizeBytes: 10, BlockSize: 1, StoreID: "s" + taken, WrittenBytes: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.SetPointCheck(ctx, "system", p.ID, check, "test"); err != nil {
			t.Fatal(err)
		}
		return p
	}
	test := func(p Point, verdict string) RestoreTest {
		j, _ := d.CreateJob(ctx, "system", "restore_test", &v.ID, "schedule")
		rt, err := d.CreateRestoreTest(ctx, "system", RestoreTest{VMID: v.ID, PointID: p.ID, JobID: j.ID})
		if err != nil {
			t.Fatal(err)
		}
		var rto *float64
		if verdict == "OK" {
			x := 42.5
			rto = &x
		}
		rt, err = d.FinishRestoreTest(ctx, "system", rt.ID, verdict, rto, verdict+" because", "b-"+verdict, map[string]any{"ready_s": 1})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	p1 := point("2026-11-16T10:00:00Z", true)
	if string(p1.Machine) != `{"mac":"52:54:00:00:00:01"}` {
		t.Fatalf("machine %s", p1.Machine)
	}
	t1 := test(p1, "OK")
	if t1.ProvenRTOS == nil || *t1.ProvenRTOS != 42.5 || t1.BundleID != "b-OK" || t1.Reason != "OK because" || t1.FinishedAt == "" {
		t.Fatalf("finished test %+v", t1)
	}
	if _, err := d.FinishRestoreTest(ctx, "system", t1.ID, "OK", nil, "", "", nil); err == nil {
		t.Fatal("a test finished twice")
	}
	p2 := point("2026-11-16T11:00:00Z", true)
	t2 := test(p2, "CRITICAL")
	f, _ = d.StatusFacts(ctx, v.ID)
	if f.NewestChecked.ID != p2.ID || f.LatestTest.ID != t2.ID || f.LatestPoint.ID != p2.ID || f.Passing.ID != t1.ID || f.PassingPoint.ID != p1.ID {
		t.Fatalf("facts %+v", f)
	}
	// A verification finds p1 damaged: its pass no longer proves anything, and p1 is a damaged point.
	if _, err := d.SetPointCheck(ctx, "system", p1.ID, false, "verification 1"); err != nil {
		t.Fatal(err)
	}
	f, _ = d.StatusFacts(ctx, v.ID)
	if f.Passing != nil || !slices.Equal(f.Damaged, []int64{p1.ID}) {
		t.Fatalf("after damage: passing %+v damaged %v", f.Passing, f.Damaged)
	}
	// Pruned: neither damaged any more nor proof.
	if err := d.PrunePoint(ctx, "system", p1.ID, "retention"); err != nil {
		t.Fatal(err)
	}
	if f, _ = d.StatusFacts(ctx, v.ID); f.Passing != nil || len(f.Damaged) != 0 {
		t.Fatalf("after prune: %+v", f)
	}

	// Status history: a change is recorded once, the same status again is not.
	for i, c := range []struct {
		st      string
		changed bool
	}{{"UNKNOWN", true}, {"UNKNOWN", false}, {"OK", true}, {"CRITICAL", true}, {"CRITICAL", false}} {
		ch, err := d.SetVMStatus(ctx, v.ID, c.st, "reason "+c.st, map[string]any{"i": i})
		if err != nil || ch != c.changed {
			t.Fatalf("set %d %s: changed %v %v", i, c.st, ch, err)
		}
	}
	ss, _ := d.VMStatuses(ctx, v.ID)
	if len(ss) != 1 || ss[0].Status != "CRITICAL" || string(ss[0].Result) != `{"i":4}` {
		t.Fatalf("stored %+v", ss)
	}
	all, _ := d.StateChanges(ctx, "vm_status", v.ID, 0, 100)
	var hist []string
	for _, c := range all {
		hist = append(hist, c.From+">"+c.To)
	}
	if !slices.Equal(hist, []string{">UNKNOWN", "UNKNOWN>OK", "OK>CRITICAL"}) {
		t.Fatalf("history %v", hist)
	}
	tests, _ := d.StateChanges(ctx, "restore_test", t2.ID, 0, 100)
	if len(tests) != 2 || tests[1].From != "UNKNOWN" || tests[1].To != "CRITICAL" || tests[1].Reason != "CRITICAL because" {
		t.Fatalf("test history %+v", tests)
	}
}
