package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// host_key_changed (ADR-007 C18.2): a host presenting another SSH key than the pinned one is a security event at
// once, with the host, both fingerprints and when the pinned key was pinned, and tells the admin to check and accept
// the key on the Hosts page (a step-up). The same key seen again is no second event; a channel without the security
// category does not get it.
func TestHostKeyChangedEvent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	hook, _ := notifyChannels(t, f)
	other := newWebhookSink(t)
	var oc ChannelCreated
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/notification-channels",
		fmt.Sprintf(`{"name":"nosec","kind":"webhook","categories":["status","jobs"],"webhook":{"url":%q,"ca_pem":%q}}`, other.srv.URL+"/gaia", other.caPEM()),
		&oc); st != http.StatusCreated {
		t.Fatalf("channel without security: %d", st)
	}
	other.secret = oc.WebhookSecret
	h, err := f.s.db.CreateHost(ctx, store.HostInput{Name: "kvm-07", Address: "kvm-07.lab", SSHUser: "gaia"})
	if err != nil {
		t.Fatal(err)
	}
	if h, err = f.s.db.PinHostKey(ctx, "admin", h.ID, "ssh-ed25519 AAAAOLD", "SHA256:old"); err != nil {
		t.Fatal(err)
	}
	changed := &hostconn.HostKeyChangedError{Presented: hostconn.Scanned{Fingerprint: "SHA256:new", Type: "ssh-ed25519", Key: "ssh-ed25519 AAAANEW"},
		PinnedFingerprint: "SHA256:old", PresentedFingerprint: "SHA256:new"}
	for range 2 { // a backup and a restore test both meet it: one event
		if err := f.s.hostKeyPresented(ctx, h, changed); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.notifyOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	h2, err := f.s.db.Host(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	hook.mu.Lock()
	var got []Event
	for _, e := range hook.events {
		if e.Event == evHostKey {
			got = append(got, e)
		}
	}
	bad := hook.bad
	hook.mu.Unlock()
	if len(got) != 1 || len(bad) > 0 {
		t.Fatalf("host_key_changed events %+v (problems %v)", got, bad)
	}
	e := got[0]
	want := HostKeyEvent{HostID: h.ID, Host: "kvm-07", PinnedFingerprint: "SHA256:old", PinnedAt: h.SSHHostKeyPinnedAt, PresentedFingerprint: "SHA256:new",
		PresentedAt: e.At}
	if e.HostKey == nil || *e.HostKey != want || h.SSHHostKeyPinnedAt == "" || h2.PresentedAt == "" || e.Change == nil || e.Change.Entity != "host_key" {
		t.Fatalf("event %+v %+v, want %+v", e, e.HostKey, want)
	}
	for _, s := range []string{"kvm-07", "SHA256:old", "SHA256:new", "pinned on " + h.SSHHostKeyPinnedAt, "Hosts page",
		fmt.Sprintf("gaia hosts accept-key %d fingerprint=SHA256:new", h.ID), "second factor", "paused"} {
		if !strings.Contains(e.Subject+"\n"+e.Text, s) {
			t.Errorf("the message does not say %q:\n%s\n%s", s, e.Subject, e.Text)
		}
	}
	other.mu.Lock()
	defer other.mu.Unlock()
	for _, e := range other.events {
		if e.Event == evHostKey {
			t.Errorf("a channel without security got %+v", e)
		}
	}
}

// ADR-007 C18.4, ADR-008 §4 amendment 8: removing a category from a channel, turning its daily report off (it weakens
// who is told, like a category) or turning the channel off needs a step-up bound to the action; adding a category,
// turning the daily report or the channel on, or giving the same categories again, does not. A refused change
// changes nothing.
func TestChannelWeakeningNeedsStepUp(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.tokens[store.RoleAdmin]
	var ch ChannelCreated
	if st := f.do(admin, "POST", "/api/v1/notification-channels",
		`{"name":"ops","kind":"webhook","webhook":{"url":"https://hooks.example.org/gaia"},"categories":["status","security"]}`, &ch); st != http.StatusCreated {
		t.Fatalf("create: %d", st)
	}
	path := fmt.Sprintf("/api/v1/notification-channels/%d", ch.ID)
	if rt := f.s.stepUpRoute("PATCH", path); rt == nil || rt.stepUp != stepIfLoose {
		t.Fatalf("PATCH %s is not a step-up route", path)
	}
	before, err := f.s.db.Channel(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"categories":["status"]}`,                         // security removed
		`{"categories":["status","jobs","integrity"]}`,      // security removed, two added
		`{"daily_report":false}`,                            // the daily report off
		`{"enabled":false,"reason":"mail server replaced"}`, // the channel off
		`{"categories":["status","security","migration"],"enabled":false}`,
	} {
		if c, e := f.call(admin, "PATCH", path, body, nil, nil); c != http.StatusForbidden || e.Code != "step_up_required" || !strings.Contains(e.Error, "weakens who is told") {
			t.Errorf("PATCH %s without a step-up: %d %+v", body, c, e)
		}
	}
	if now, err := f.s.db.Channel(ctx, ch.ID); err != nil || !reflect.DeepEqual(now, before) {
		t.Fatalf("a refused change changed the channel: %+v %v", now, err)
	}
	// A step-up for another action does not pass it.
	if c, e := f.call(admin, "PATCH", path, `{"enabled":false}`, f.stepUp(admin, "GET", "/api/v1/evidence-export"), nil); c != http.StatusForbidden || e.Code != "step_up_failed" {
		t.Errorf("a step-up for another action: %d %+v", c, e)
	}
	var out store.Channel
	for _, tc := range []struct {
		body string
		cats []string
	}{
		{`{"categories":["security","status"]}`, []string{catStatus, catSecurity}},                           // the same again
		{`{"categories":["status","security","migration"]}`, []string{catStatus, catSecurity, catMigration}}, // one added
		{`{"daily_report":true,"enabled":true}`, []string{catStatus, catSecurity, catMigration}},             // on, as they are
	} {
		if c, e := f.call(admin, "PATCH", path, tc.body, nil, &out); c != http.StatusOK || !slices.Equal(out.Categories, tc.cats) {
			t.Fatalf("PATCH %s without a step-up: %d %+v %+v", tc.body, c, e, out)
		}
	}
	if c, e := f.call(admin, "PATCH", path, `{"categories":["status"],"daily_report":false,"reason":"status only"}`, f.stepUp(admin, "PATCH", path), &out); c != http.StatusOK ||
		!slices.Equal(out.Categories, []string{catStatus}) || out.DailyReport {
		t.Fatalf("with a step-up: %d %+v %+v", c, e, out)
	}
	if c, e := f.call(admin, "PATCH", path, `{"enabled":false}`, f.stepUp(admin, "PATCH", path), &out); c != http.StatusOK || out.Enabled {
		t.Fatalf("off with a step-up: %d %+v %+v", c, e, out)
	}
	if c, e := f.call(admin, "PATCH", path, `{"enabled":true}`, nil, &out); c != http.StatusOK || !out.Enabled {
		t.Fatalf("on again: %d %+v %+v", c, e, out)
	}
	if !hasAudit(f.audits(t), "step_up", "step-up passed") {
		t.Error("no audit entry of the passed step-up")
	}
	sc, err := f.s.db.StateChanges(ctx, store.ChannelCategoriesEntity, ch.ID, 0, 10)
	if err != nil || len(sc) != 3 || sc[2].Reason != "status only" || sc[2].To != `{"categories":["status"],"daily_report":false}` {
		t.Fatalf("recorded changes %+v %v", sc, err)
	}
	// The store refuses a change decided against a channel that changed meanwhile.
	if _, err := f.s.db.UpdateChannel(ctx, "admin", ch.ID, before, store.ChannelChange{Categories: []string{catStatus, catSecurity}}, "x"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a stale change: %v", err)
	}
	if !slices.Contains(stepUpActions(f.s.routes), "PATCH /api/v1/notification-channels/{id} (when it weakens protection)") {
		t.Errorf("the settings screen's list misses the channel change: %v", stepUpActions(f.s.routes))
	}
}
