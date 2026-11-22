package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// A host whose SSH key was never trusted (ADR-007 C11): a backup of one of its VMs fails with the structured
// host_key_untrusted flag (hostconn.ErrUntrusted, not the message), its VMs are UNKNOWN with host_key_untrusted
// instead of never_backed_up, and "Needs attention" shows them as one (code, host) group. Once the key is trusted,
// the code is gone.
func TestHostKeyUntrustedGroup(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	pol := f.ids["policies"]
	h, err := f.s.db.CreateHost(ctx, store.HostInput{Name: "kvm-new", Address: "kvm-new.lab", SSHUser: "gaia"})
	if err != nil {
		t.Fatal(err)
	}
	var vms []int64
	for i := range 3 {
		v, err := f.s.db.CreateVM(ctx, "admin", store.VMInput{HostID: h.ID, Name: fmt.Sprintf("new-%d", i), PolicyID: &pol})
		if err != nil {
			t.Fatal(err)
		}
		vms = append(vms, v.ID)
	}
	j, err := f.s.db.CreateJob(ctx, "system", "backup", &vms[0], "test")
	if err != nil {
		t.Fatal(err)
	}
	f.s.runBackup(ctx, j)
	ji, err := f.s.db.JobInfo(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var d BackupDetail
	if err := json.Unmarshal(ji.Detail, &d); err != nil || !d.HostKeyUntrusted || d.Unreachable {
		t.Fatalf("the backup's detail: %+v %v", d, err)
	}
	for _, id := range vms {
		r, err := f.s.refreshStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != status.Unknown || r.Reasons[0].Code != status.HostKeyUntrusted || r.Reasons[0].Scope == nil || r.Reasons[0].Scope.ID != h.ID {
			t.Fatalf("VM %d: %s %+v", id, r.Status, r.Reasons)
		}
	}
	var av AttentionView
	if code := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/attention", "", &av); code != 200 {
		t.Fatalf("attention: %d", code)
	}
	// One group per untrusted host (the fixture's seed host has no key either).
	n := 0
	for _, g := range av.Groups {
		if g.Code == string(status.HostKeyUntrusted) && g.Scope != nil && g.Scope.ID == h.ID {
			n++
			if g.Count != 3 || g.Scope.Kind != status.ScopeHost || g.Status != status.Unknown || g.Cause != "Host key not trusted yet" {
				t.Fatalf("group %+v", g)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d host_key_untrusted groups for kvm-new: %+v", n, av.Groups)
	}
	// Trusted: the VMs are never backed up (the failed job's cause is gone), not host_key_untrusted.
	if _, err := f.s.db.PinHostKey(ctx, "admin", h.ID, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixtureKeyFixtureKeyFixtureKeyFixtureKey1", "SHA256:fixture"); err != nil {
		t.Fatal(err)
	}
	for _, id := range vms {
		r, err := f.s.refreshStatus(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Reasons[0].Code == status.HostKeyUntrusted {
			t.Fatalf("VM %d after the key was trusted: %+v", id, r.Reasons)
		}
	}
}
