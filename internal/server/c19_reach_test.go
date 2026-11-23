package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// A host-scoped cause shows its full reach (ADR-007 C19.2): a host whose SSH key changed holds six VMs; three are
// grouped under host_key_changed, two under no_policy (UNKNOWN) and one under a failed restore test (CRITICAL). The
// host_key_changed group still counts its own three, says the host has six with three under other causes, and its
// "Show these VMs" filter (reach=true) lists all six; without reach the cause filter lists its three. A group that is
// not host-scoped has no reach, and reach with such a code is refused.
func TestHostScopedReach(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	clock := time.Date(2026, 11, 16, 12, 30, 0, 0, time.UTC) // the seed point is from 12:00; the seed policy's RPO is 1 h
	f.s.now = func() time.Time { return clock }
	f.s.db.SetClock(func() time.Time { return clock })
	seed, pol := f.ids["vms"], f.ids["policies"]
	v, err := f.s.db.VM(ctx, seed)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.s.db.PinHostKey(ctx, "admin", v.HostID, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPinned", "SHA256:pinned")
	if err != nil {
		t.Fatal(err)
	}
	pts, _ := f.s.db.Points(ctx, seed, 1)
	if _, err := f.s.db.SetPointCheck(ctx, "system", pts[0].ID, true, "seed"); err != nil {
		t.Fatal(err)
	}
	tests, err := f.s.db.RestoreTests(ctx, seed, 1)
	if err != nil || len(tests) != 1 {
		t.Fatalf("the seed test: %+v %v", tests, err)
	}
	rto := 60.0
	if _, err := f.s.db.FinishRestoreTest(ctx, "system", tests[0].ID, "OK", &rto, "passed", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	vm := func(host int64, name string, policy *int64) int64 {
		t.Helper()
		x, err := f.s.db.CreateVM(ctx, "admin", store.VMInput{HostID: host, Name: name, PolicyID: policy})
		if err != nil {
			t.Fatal(err)
		}
		return x.ID
	}
	vm(h.ID, "paused-1", &pol) // no point yet: host_key_changed (UNKNOWN, no_checked_point) once the key changed
	vm(h.ID, "paused-2", &pol)
	vm(h.ID, "nopol-1", nil) // no policy: no_policy decides
	vm(h.ID, "nopol-2", nil)
	// A checked point within the RPO target and a CRITICAL restore test: the test decides, not the paused backups.
	failing := vm(h.ID, "failing", &pol)
	j, err := f.s.db.CreateJob(ctx, "system", "backup", &failing, "test")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := f.s.db.Policy(ctx, pol)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := f.s.db.CreatePoint(ctx, "system", store.Point{VMID: failing, JobID: j.ID, RepositoryID: repo.RepositoryID, Kind: "full", TakenAt: "2026-11-16T12:10:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.SetPointCheck(ctx, "system", pt.ID, true, "test"); err != nil {
		t.Fatal(err)
	}
	rt, err := f.s.db.CreateRestoreTest(ctx, "system", store.RestoreTest{VMID: failing, PointID: pt.ID, JobID: j.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.db.FinishRestoreTest(ctx, "system", rt.ID, "CRITICAL", nil, "a check failed", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	// Another host, never trusted: a host-scoped group of its own, whose reach is its one VM.
	other, err := f.s.db.CreateHost(ctx, store.HostInput{Name: "kvm-other", Address: "kvm-other.lab", SSHUser: "gaia"})
	if err != nil {
		t.Fatal(err)
	}
	vm(other.ID, "other-1", &pol)

	seen := &hostconn.HostKeyChangedError{Presented: hostconn.Scanned{Key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINew", Fingerprint: "SHA256:new", Type: "ssh-ed25519"},
		PinnedFingerprint: "SHA256:pinned", PresentedFingerprint: "SHA256:new"}
	if err := f.s.hostKeyPresented(ctx, h, seen); err != nil {
		t.Fatal(err)
	}
	if err := f.s.refreshStatuses(ctx, clock); err != nil {
		t.Fatal(err)
	}

	var av AttentionView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/attention", "", &av); st != 200 {
		t.Fatalf("attention: %d", st)
	}
	group := func(code status.Code, host int64) AttentionGroup {
		t.Helper()
		for _, g := range av.Groups {
			if g.Code == string(code) && (host == 0 && g.Scope == nil || g.Scope != nil && g.Scope.ID == host) {
				return g
			}
		}
		t.Fatalf("no %s group for host %d: %+v", code, host, av.Groups)
		return AttentionGroup{}
	}
	hk := group(status.HostKeyChanged, h.ID)
	if hk.Count != 3 || hk.Status != status.Critical || hk.Reach == nil || *hk.Reach != (AttentionReach{HostVMs: 6, Elsewhere: 3, OK: 0}) {
		t.Fatalf("host_key_changed: count %d, reach %+v", hk.Count, hk.Reach)
	}
	if g := group(status.NoPolicy, 0); g.Count != 2 || g.Reach != nil {
		t.Fatalf("no_policy, not host-scoped: %+v", g)
	}
	if g := group(status.TestFailedCheck, 0); g.Count != 1 || g.Reach != nil {
		t.Fatalf("test_failed_check: %+v", g)
	}
	if g := group(status.HostKeyUntrusted, other.ID); g.Count != 1 || g.Reach == nil || *g.Reach != (AttentionReach{HostVMs: 1}) {
		t.Fatalf("host_key_untrusted on kvm-other: count %d, reach %+v", g.Count, g.Reach)
	}

	list := func(q string) (int, VMListView) {
		t.Helper()
		var l VMListView
		st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/vm-list?"+q, "", &l)
		return st, l
	}
	cause := fmt.Sprintf("code=%s&scope_kind=host&scope_id=%d", status.HostKeyChanged, h.ID)
	if st, l := list(cause); st != 200 || l.Total != 3 {
		t.Fatalf("the cause's own VMs: %d %+v", st, l.Total)
	}
	st, l := list(cause + "&reach=true")
	if st != 200 || l.Total != hk.Reach.HostVMs || len(l.Rows) != 6 {
		t.Fatalf("reach: %d total %d rows %d", st, l.Total, len(l.Rows))
	}
	codes := map[string]int{}
	for _, r := range l.Rows {
		if r.HostID != h.ID {
			t.Fatalf("reach listed %s on host %d", r.VM, r.HostID)
		}
		codes[r.Code]++
	}
	if codes[string(status.HostKeyChanged)] != 3 || codes[string(status.NoPolicy)] != 2 || codes[string(status.TestFailedCheck)] != 1 {
		t.Fatalf("reach by code: %v", codes)
	}
	// Pages of the reach add up to it.
	st, p1 := list(cause + "&reach=true&limit=4")
	if st != 200 || len(p1.Rows) != 4 || p1.Next == "" {
		t.Fatalf("reach page 1: %d %d %q", st, len(p1.Rows), p1.Next)
	}
	if st, p2 := list(cause + "&reach=true&limit=4&after=" + p1.Next); st != 200 || len(p2.Rows) != 2 || p2.Total != 6 {
		t.Fatalf("reach page 2: %d %+v", st, p2)
	}
	// Reach is only for a host-scoped cause on a host.
	for _, q := range []string{"code=no_policy&reach=true", fmt.Sprintf("code=no_policy&scope_kind=host&scope_id=%d&reach=true", h.ID),
		fmt.Sprintf("code=%s&reach=true", status.HostKeyChanged), fmt.Sprintf("scope_kind=host&scope_id=%d&reach=true", h.ID)} {
		if st, _ := list(q); st != 400 {
			t.Fatalf("%s: %d, want 400", q, st)
		}
	}
}
