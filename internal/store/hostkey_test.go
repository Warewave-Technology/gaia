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
