package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A host's key (ADR-007 C17): pinning records when; a different key the host presents is kept (once, with a state
// change) and never pinned by that; a pinned key is never replaced by PinHostKey; accepting needs the presented key's
// fingerprint, pins it with the time, clears the presented key and is a state change.
func TestHostKeyChangedAndAccepted(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	h, err := d.CreateHost(ctx, HostInput{Name: "kvm-07", Address: "10.0.0.7", SSHUser: "gaia"})
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := d.HostKeyPresented(ctx, h.ID, "ssh-ed25519 NEW", "SHA256:new", "SHA256:old"); err != nil || rec {
		t.Fatalf("a host with no pinned key: %v %v", rec, err)
	}
	p, err := d.PinHostKey(ctx, "admin", h.ID, "ssh-ed25519 OLD", "SHA256:old")
	if err != nil || p.SSHHostKey != "ssh-ed25519 OLD" || p.SSHHostKeyPinnedAt == "" {
		t.Fatalf("pin: %+v %v", p, err)
	}
	if _, err := d.PinHostKey(ctx, "operator1", h.ID, "ssh-ed25519 NEW", "SHA256:new"); !errors.Is(err, ErrConflict) {
		t.Fatalf("PinHostKey replaced a pinned key: %v", err)
	}
	if again, err := d.PinHostKey(ctx, "admin", h.ID, "ssh-ed25519 OLD", "SHA256:old"); err != nil || again.SSHHostKeyPinnedAt != p.SSHHostKeyPinnedAt {
		t.Fatalf("the same key again: %+v %v", again, err)
	}
	for i, want := range []bool{true, false} { // the same presented key twice: recorded once
		rec, err := d.HostKeyPresented(ctx, h.ID, "ssh-ed25519 NEW", "SHA256:new", "SHA256:old")
		if err != nil || rec != want {
			t.Fatalf("presented %d: %v %v", i, rec, err)
		}
	}
	h2, _ := d.Host(ctx, h.ID)
	if h2.SSHHostKey != "ssh-ed25519 OLD" || h2.PresentedHostKey != "ssh-ed25519 NEW" || h2.PresentedFingerprint != "SHA256:new" || h2.PresentedAt == "" {
		t.Fatalf("after the host presented another key: %+v", h2)
	}
	if pre, err := d.StateChanges(ctx, "host_key", h.ID, 0, 10); err != nil || len(pre) != 2 {
		t.Fatalf("state changes %+v %v", pre, err)
	} else if k, err := d.HostKeyChangeOf(ctx, pre[1]); err != nil || k.PinnedAt != p.SSHHostKeyPinnedAt || k.PresentedAt != pre[1].At {
		t.Fatalf("the change read back before it was accepted: %+v %v (pinned at %s)", k, err, p.SSHHostKeyPinnedAt)
	}
	if rec, err := d.HostKeyPresented(ctx, h.ID, "ssh-ed25519 OLD", "SHA256:old", "SHA256:old"); err != nil || rec {
		t.Fatalf("the pinned key itself: %v %v", rec, err)
	}
	if _, err := d.AcceptPresentedHostKey(ctx, "admin", h.ID, "SHA256:other", "SHA256:old"); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepting another fingerprint than the presented one: %v", err)
	}
	a, err := d.AcceptPresentedHostKey(ctx, "admin", h.ID, "SHA256:new", "SHA256:old")
	if err != nil || a.SSHHostKey != "ssh-ed25519 NEW" || a.PresentedHostKey != "" || a.PresentedFingerprint != "" || a.PresentedAt != "" ||
		a.SSHHostKeyPinnedAt < h2.PresentedAt {
		t.Fatalf("accepted: %+v %v", a, err)
	}
	if _, err := d.AcceptPresentedHostKey(ctx, "admin", h.ID, "SHA256:new", "SHA256:old"); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepting twice: %v", err)
	}
	cs, err := d.StateChanges(ctx, "host_key", h.ID, 0, 10)
	if err != nil || len(cs) != 3 {
		t.Fatalf("state changes %+v %v", cs, err)
	}
	for i, w := range [][3]string{{"untrusted", "trusted", "admin"}, {"trusted", "changed", "system"}, {"changed", "trusted", "admin"}} {
		if cs[i].From != w[0] || cs[i].To != w[1] || cs[i].Actor != w[2] || cs[i].At == "" {
			t.Fatalf("state change %d: %+v, want %v", i, cs[i], w)
		}
	}
	// The change, read back for the host_key_changed notification (ADR-007 C18.2): both fingerprints, when Gaia saw the
	// presented key, and when the key it is not was pinned, also once a later key was accepted.
	k, err := d.HostKeyChangeOf(ctx, cs[1])
	if err != nil || k != (HostKeyChange{HostID: h.ID, PresentedFingerprint: "SHA256:new", PresentedAt: cs[1].At, PinnedFingerprint: "SHA256:old", PinnedAt: cs[0].At}) {
		t.Fatalf("the change read back: %+v %v", k, err)
	}
	if _, err := d.HostKeyChangeOf(ctx, cs[0]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a trusted key read as a change: %v", err)
	}
	if rec, err := d.HostKeyPresented(ctx, h.ID, "ssh-ed25519 NEWER", "SHA256:newer", "SHA256:new"); err != nil || !rec {
		t.Fatalf("a newer key: %v %v", rec, err)
	}
	cs, err = d.StateChanges(ctx, "host_key", h.ID, 0, 10)
	if err != nil || len(cs) != 4 {
		t.Fatalf("state changes %+v %v", cs, err)
	}
	if k, err := d.HostKeyChangeOf(ctx, cs[3]); err != nil || k.PinnedFingerprint != "SHA256:new" || k.PresentedFingerprint != "SHA256:newer" || k.PinnedAt != a.SSHHostKeyPinnedAt {
		t.Fatalf("the newer key read back: %+v %v", k, err)
	}
	if k, err := d.HostKeyChangeOf(ctx, cs[1]); err != nil || k.PinnedAt != cs[0].At {
		t.Fatalf("the first change after a later accept: %+v %v", k, err)
	}
	// A key pinned before its pin was recorded (schema 15's upgrade): not known when a key was trusted since, else the
	// host's pinned time.
	old, err := d.CreateHost(ctx, HostInput{Name: "kvm-08", Address: "10.0.0.8", SSHUser: "gaia"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL().ExecContext(ctx, `UPDATE hosts SET ssh_host_key = 'ssh-ed25519 OLD8', ssh_host_key_pinned_at = '2026-11-01T08:00:00Z' WHERE id = ?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.HostKeyPresented(ctx, old.ID, "ssh-ed25519 NEW8", "SHA256:new8", "SHA256:old8"); err != nil {
		t.Fatal(err)
	}
	cs, err = d.StateChanges(ctx, "host_key", old.ID, 0, 10)
	if err != nil || len(cs) != 1 {
		t.Fatalf("state changes %+v %v", cs, err)
	}
	if k, err := d.HostKeyChangeOf(ctx, cs[0]); err != nil || k.PinnedAt != "2026-11-01T08:00:00Z" {
		t.Fatalf("no recorded pin: %+v %v", k, err)
	}
	if _, err := d.AcceptPresentedHostKey(ctx, "admin", old.ID, "SHA256:new8", "SHA256:old8"); err != nil {
		t.Fatal(err)
	}
	if k, err := d.HostKeyChangeOf(ctx, cs[0]); err != nil || k.PinnedAt != "" {
		t.Fatalf("no recorded pin, a key accepted since: %+v %v", k, err)
	}
}

// The last known slot count (ADR-007 C17): none until the helper reported one; each report updates the time; a changed
// count is a state change.
func TestSandboxSlotsLastKnown(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	if _, ok, err := d.LastSandboxSlots(ctx); ok || err != nil {
		t.Fatalf("before any report: %v %v", ok, err)
	}
	at := time.Date(2026, 11, 23, 14, 2, 0, 0, time.UTC)
	for i, n := range []int{2, 2, 4} {
		if err := d.SetSandboxSlots(ctx, n, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	s, ok, err := d.LastSandboxSlots(ctx)
	if err != nil || !ok || s.Slots != 4 || s.ReportedAt != "2026-11-23T14:04:00Z" {
		t.Fatalf("last known %+v %v %v", s, ok, err)
	}
	cs, err := d.StateChanges(ctx, "sandbox_slots", 0, 0, 10)
	if err != nil || len(cs) != 2 || cs[0].From != "" || cs[0].To != "2" || cs[1].From != "2" || cs[1].To != "4" {
		t.Fatalf("state changes %+v %v", cs, err)
	}
	if err := d.SetSandboxSlots(ctx, -1, at); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative slots: %v", err)
	}
}
