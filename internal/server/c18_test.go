package server

import (
	"context"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// A changed host key acts at once (ADR-007 C18.1): an OK VM whose host presents another SSH key than the pinned one is
// WARNING (host_key_changed, rule backups_paused) at the next tick after the change is seen, before any backup job
// failed on it; its host is a CRITICAL group in "Needs attention" at once although the VM is WARNING; the status
// table agrees with the status model (GET /api/v1/status/check); the VM's next change is its RPO target, where it
// turns CRITICAL with the same code. Accepting the key ends it.
func TestHostKeyChangedActsAtOnce(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	clock := time.Date(2026, 11, 16, 12, 30, 0, 0, time.UTC) // the seed point is from 12:00; the seed policy's RPO is 1 h
	f.s.now = func() time.Time { return clock }
	f.s.db.SetClock(func() time.Time { return clock })
	vm := f.ids["vms"]
	v, err := f.s.db.VM(ctx, vm)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.s.db.PinHostKey(ctx, "admin", v.HostID, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPinned", "SHA256:pinned")
	if err != nil {
		t.Fatal(err)
	}
	pts, _ := f.s.db.Points(ctx, vm, 1)
	if _, err := f.s.db.SetPointCheck(ctx, "system", pts[0].ID, true, "seed"); err != nil {
		t.Fatal(err)
	}
	tests, err := f.s.db.RestoreTests(ctx, vm, 1)
	if err != nil || len(tests) != 1 {
		t.Fatalf("the seed test: %+v %v", tests, err)
	}
	rto := 60.0
	if _, err := f.s.db.FinishRestoreTest(ctx, "system", tests[0].ID, "OK", &rto, "passed", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.refreshStatuses(ctx, clock); err != nil {
		t.Fatal(err)
	}
	// The check GET /api/v1/status/check runs (called directly once the clock jumped: a jump ends the test's sessions).
	check := func(when string) {
		t.Helper()
		if chk, err := f.s.checkStatuses(ctx); err != nil || chk.Verdict != "OK" || chk.Equal != chk.VMs || chk.VMs != 1 {
			t.Fatalf("%s: status check %+v %v", when, chk, err)
		}
	}
	row := func() store.VMStatus {
		t.Helper()
		rows, err := f.s.db.VMStatuses(ctx, vm)
		if err != nil || len(rows) != 1 {
			t.Fatalf("status row: %+v %v", rows, err)
		}
		return rows[0]
	}
	if r := row(); r.Status != status.OK {
		t.Fatalf("before the change: %+v", r)
	}
	check("before the change")

	// A host check (no backup job) sees another key.
	seen := &hostconn.HostKeyChangedError{Presented: hostconn.Scanned{Key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINew", Fingerprint: "SHA256:new", Type: "ssh-ed25519"},
		PinnedFingerprint: "SHA256:pinned", PresentedFingerprint: "SHA256:new"}
	if err := f.s.hostKeyPresented(ctx, h, seen); err != nil {
		t.Fatal(err)
	}
	if chk, _ := f.s.checkStatuses(ctx); chk.Due != 1 || len(chk.Differ) != 0 {
		t.Fatalf("due at the next tick: %+v", chk)
	}
	if err := f.s.refreshStatuses(ctx, clock); err != nil {
		t.Fatal(err)
	}
	r, _, err := f.s.storedStatus(ctx, vm)
	if err != nil {
		t.Fatal(err)
	}
	x := r.Reasons[0]
	if r.Status != status.Warning || x.Code != status.HostKeyChanged || x.Rule != status.RuleBackupsPaused || x.Status != status.Warning ||
		x.Scope == nil || x.Scope.Kind != status.ScopeHost || x.Scope.ID != h.ID || x.Params.Fingerprint != "SHA256:new" || x.Params.PinnedAt == nil {
		t.Fatalf("after the change: %s %+v", r.Status, r.Reasons)
	}
	var chk StatusCheck
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/status/check", "", &chk); st != 200 || chk.Verdict != "OK" || chk.Equal != 1 {
		t.Fatalf("after the change: status check %d %+v", st, chk)
	}
	var fl FleetView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/fleet", "", &fl); st != 200 || fl.ByStatus[status.Warning] != 1 || fl.Causes != 1 ||
		fl.CodesVer != 5 {
		t.Fatalf("fleet: %d %+v", st, fl)
	}
	var av AttentionView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/attention", "", &av); st != 200 || len(av.Groups) != 1 {
		t.Fatalf("attention: %d %+v", st, av)
	}
	if g := av.Groups[0]; g.Code != string(status.HostKeyChanged) || g.Status != status.Critical || g.Count != 1 || g.Scope == nil || g.Scope.ID != h.ID ||
		g.Params == nil || g.Params.Fingerprint != "SHA256:new" {
		t.Fatalf("the host's group, CRITICAL while its VM is WARNING: %+v", g)
	}

	// WARNING -> CRITICAL exactly at the RPO target: 12:00 + 1 h.
	next := row().NextChangeAt
	if want := time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC); next == nil || !timeOf(*next).Equal(want) {
		t.Fatalf("next change %v, want %v", next, want)
	}
	clock = timeOf(*next).Add(time.Second)
	if err := f.s.refreshStatuses(ctx, clock); err != nil {
		t.Fatal(err)
	}
	if r := row(); r.Status != status.Critical || r.Code != string(status.HostKeyChanged) {
		t.Fatalf("after the RPO target: %+v", r)
	}
	check("after the RPO target")

	// Accepted (the route marks the host's VMs due): the cause is gone.
	if _, err := f.s.db.AcceptPresentedHostKey(ctx, "admin", h.ID, "SHA256:new", "SHA256:pinned"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.MarkStatusesDue(ctx, h.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.s.refreshStatuses(ctx, clock); err != nil {
		t.Fatal(err)
	}
	if r := row(); r.Code == string(status.HostKeyChanged) {
		t.Fatalf("after the key was accepted: %+v", r)
	}
	check("after the key was accepted")
}
