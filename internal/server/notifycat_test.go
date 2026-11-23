package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
)

// Every event sent by category has one of the five (ADR-007 C8); the daily report and the test message have none.
func TestEventCategories(t *testing.T) {
	for _, ev := range []string{evStatus, evJobFailed, evIntegrity, evQuarantine, evLedger, evFullBackup, evBreakglass, evNewDevice, evLocked,
		evSettings, evCapacityShort, evMigrationCheck} {
		if !slices.Contains(allCategories, eventCategory[ev]) {
			t.Errorf("event %s: category %q", ev, eventCategory[ev])
		}
		if !slices.Contains(allEvents, ev) {
			t.Errorf("event %s is not in allEvents", ev)
		}
	}
	for _, ev := range []string{evDaily, evTest} {
		if _, ok := eventCategory[ev]; ok {
			t.Errorf("%s has a category", ev)
		}
	}
	if len(allEvents) != len(eventCategory)+1 {
		t.Errorf("allEvents %v: every categorised event and daily_report", allEvents)
	}
	want := map[string]string{evStatus: catStatus, evJobFailed: catJobs, evFullBackup: catJobs, evCapacityShort: catJobs,
		evIntegrity: catIntegrity, evQuarantine: catIntegrity, evLedger: catIntegrity, evBreakglass: catSecurity, evNewDevice: catSecurity,
		evLocked: catSecurity, evSettings: catSecurity, evMigrationCheck: catMigration}
	for ev, cat := range want {
		if eventCategory[ev] != cat {
			t.Errorf("%s: %s, want %s", ev, eventCategory[ev], cat)
		}
	}
	for _, tc := range []struct {
		events []string
		cats   []string
		daily  bool
	}{
		{[]string{evQuarantine}, []string{catIntegrity}, false},
		{[]string{evDaily, evStatus, evLedger, evIntegrity}, []string{catStatus, catIntegrity}, true},
		{[]string{evLocked, evCapacityShort, evStatus}, []string{catStatus, catJobs, catSecurity}, false},
		{[]string{evDaily}, []string{}, true},
	} {
		cats, daily, err := categoriesOfEvents(tc.events)
		if err != nil || !slices.Equal(cats, tc.cats) || daily != tc.daily {
			t.Errorf("events %v: %v %t %v, want %v %t", tc.events, cats, daily, err, tc.cats, tc.daily)
		}
	}
	if _, _, err := categoriesOfEvents([]string{"vm_status"}); err == nil {
		t.Error("an unknown event was taken")
	}
	if c, err := checkCategories([]string{catMigration, catStatus, catMigration}); err != nil || !slices.Equal(c, []string{catStatus, catMigration}) {
		t.Errorf("checkCategories: %v %v", c, err)
	}
	if _, err := checkCategories([]string{"daily_report"}); err == nil {
		t.Error("daily_report taken as a category")
	}
	ch := store.Channel{Categories: []string{catSecurity}, DailyReport: true}
	if !channelTakes(ch, evSettings) || channelTakes(ch, evStatus) || channelTakes(ch, evDaily) || channelTakes(ch, evTest) {
		t.Error("channelTakes")
	}
}

// Schema 14 maps an M2 event list with the same table as the server: a channel per event of M2 gets that event's
// category, and nothing else (the security events schema 11 added to every channel aside).
func TestM2EventListsMapToCategories(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx, 13); err != nil {
		t.Fatal(err)
	}
	m2 := []string{evStatus, evJobFailed, evFullBackup, evCapacityShort, evIntegrity, evQuarantine, evLedger, evBreakglass, evNewDevice, evLocked, evDaily}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, ev := range m2 {
		if _, err := db.SQL().ExecContext(ctx, `INSERT INTO notification_channels (name, kind, config, events, enabled, cursor, created_at, updated_at)
			VALUES (?, 'webhook', '{}', ?, 1, 0, ?, ?)`, "c-"+ev, fmt.Sprintf("[%q]", ev), now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Migrate(ctx, 0); err != nil {
		t.Fatal(err)
	}
	chans, err := db.Channels(ctx)
	if err != nil || len(chans) != len(m2) {
		t.Fatalf("%d channels %v", len(chans), err)
	}
	for _, c := range chans {
		ev := strings.TrimPrefix(c.Name, "c-")
		cats, daily, err := categoriesOfEvents([]string{ev})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.Categories, cats) || c.DailyReport != daily || !slices.Equal(c.M2Events, []string{ev}) {
			t.Errorf("channel of %s: categories %v, daily report %t (m2 %v); the server maps it to %v, %t", ev, c.Categories, c.DailyReport, c.M2Events, cats, daily)
		}
	}
}

// The channels API takes categories (all by default) and still the M2 events, mapped; PATCH changes them, recorded.
func TestChannelCategoriesAPI(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	admin := f.tokens[store.RoleAdmin]
	hook := `"kind":"webhook","webhook":{"url":"https://hooks.example.org/gaia"}`
	for _, tc := range []struct {
		body  string
		cats  []string
		daily bool
	}{
		{`{"name":"all",` + hook + `}`, allCategories, true},
		{`{"name":"two",` + hook + `,"categories":["security","status","security"]}`, []string{catStatus, catSecurity}, true},
		{`{"name":"noreport",` + hook + `,"categories":["migration"],"daily_report":false}`, []string{catMigration}, false},
		{`{"name":"reportonly",` + hook + `,"categories":[]}`, []string{}, true},
		{`{"name":"m2",` + hook + `,"events":["quarantine","status"]}`, []string{catStatus, catIntegrity}, false},
		{`{"name":"m2report",` + hook + `,"events":["daily_report","job_failed"]}`, []string{catJobs}, true},
		{`{"name":"m2empty",` + hook + `,"events":[]}`, allCategories, true},
	} {
		var out ChannelCreated
		if st := f.do(admin, "POST", "/api/v1/notification-channels", tc.body, &out); st != http.StatusCreated ||
			!slices.Equal(out.Categories, tc.cats) || out.DailyReport != tc.daily || len(out.M2Events) != 0 {
			t.Fatalf("%s: %d %+v, want %v %t", tc.body, st, out.Channel, tc.cats, tc.daily)
		}
	}
	for _, body := range []string{
		`{"name":"both",` + hook + `,"events":["status"],"categories":["status"]}`,
		`{"name":"badcat",` + hook + `,"categories":["vm"]}`,
		`{"name":"badev",` + hook + `,"events":["vm_status"]}`,
		`{"name":"nothing",` + hook + `,"categories":[],"daily_report":false}`,
	} {
		if st := f.do(admin, "POST", "/api/v1/notification-channels", body, nil); st != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, st)
		}
	}
	two, err := f.s.db.ChannelByName(ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/notification-channels/%d", two.ID)
	var ch store.Channel
	if st := f.do(admin, "PATCH", path, `{"categories":["migration","status"],"reason":"migration checks too"}`, &ch); st != http.StatusOK ||
		!slices.Equal(ch.Categories, []string{catStatus, catMigration}) || !ch.DailyReport || !ch.Enabled {
		t.Fatalf("PATCH categories: %d %+v", st, ch)
	}
	if st := f.do(admin, "PATCH", path, `{"daily_report":false,"enabled":false}`, &ch); st != http.StatusOK || ch.DailyReport || ch.Enabled ||
		!slices.Equal(ch.Categories, []string{catStatus, catMigration}) {
		t.Fatalf("PATCH daily report and enabled: %d %+v", st, ch)
	}
	for _, body := range []string{`{}`, `{"reason":"x"}`, `{"categories":["vm"]}`, `{"categories":[]}`} {
		if st := f.do(admin, "PATCH", path, body, nil); st != http.StatusBadRequest {
			t.Errorf("PATCH %s: %d, want 400", body, st)
		}
	}
	sc, err := f.s.db.StateChanges(ctx, store.ChannelCategoriesEntity, two.ID, 0, 10)
	if err != nil || len(sc) != 3 || sc[0].From != "" || sc[1].Reason != "migration checks too" ||
		sc[1].To != `{"categories":["status","migration"],"daily_report":true}` || sc[2].To != `{"categories":["status","migration"],"daily_report":false}` {
		t.Fatalf("recorded changes %+v %v", sc, err)
	}
	// Only the operator's and auditor's sessions: channels stay admin-only.
	if st := f.do(f.tokens[store.RoleOperator], "PATCH", path, `{"categories":["status"]}`, nil); st != http.StatusForbidden {
		t.Errorf("operator PATCH: %d", st)
	}
}

// migration_check_finished: one event for each check that ends WARNING, CRITICAL or UNKNOWN, with the VM, source,
// readiness and reason; none for OK, nor for the queued state.
func TestMigrationCheckFinishedEvent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	hook, _ := notifyChannels(t, f)
	src, err := f.s.db.CreateMigrationSource(ctx, "admin", store.MigrationSource{Name: "esxi1", Kind: "vsphere", URL: "https://esxi1.lab/sdk", Username: "root"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, r := range []string{verify.OK, verify.Warning, verify.Critical, verify.Unknown} {
		vm := "vm-" + strings.ToLower(r)
		mc, err := f.s.db.QueueMigrationCheck(ctx, "admin", src.ID, vm, nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.db.FinishMigrationCheck(ctx, "system", mc.ID, r, "because "+r, "", map[string]string{}); err != nil {
			t.Fatal(err)
		}
		ids[r] = mc.ID
	}
	for range 2 {
		if err := f.s.notifyOnce(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	hook.mu.Lock()
	defer hook.mu.Unlock()
	got := map[string]Event{}
	for _, e := range hook.events {
		if e.Event == evMigrationCheck {
			if _, dup := got[e.Migration.Readiness]; dup {
				t.Fatalf("twice: %+v", e)
			}
			got[e.Migration.Readiness] = e
		}
	}
	if len(got) != 3 || len(hook.bad) > 0 {
		t.Fatalf("migration events %v (problems %v)", got, hook.bad)
	}
	for _, r := range []string{verify.Warning, verify.Critical, verify.Unknown} {
		e := got[r]
		vm := "vm-" + strings.ToLower(r)
		if e.VM != vm || *e.Migration != (MigrationEvent{Check: ids[r], VM: vm, Source: "esxi1", Readiness: r, Reason: "because " + r}) ||
			!strings.Contains(e.Subject, "migration check of "+vm+" on esxi1: "+r) || !strings.Contains(e.Text, "Why: because "+r) {
			t.Errorf("%s: %+v %+v", r, e, e.Migration)
		}
	}
}

// settings_changed: a change of the sign-in and security settings notifies, naming what changed, not the values.
func TestSettingsChangedEvent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	hook, _ := notifyChannels(t, f)
	v := defaultSettings()
	v.IdleTimeoutMin, v.OIDC.Issuer = 30, "https://idp.lab.test/realms/gaia"
	if err := f.s.db.SetSetting(ctx, "admin", settingSignin, v); err != nil {
		t.Fatal(err)
	}
	v.PasswordMaxAge = "90d"
	if err := f.s.db.SetSetting(ctx, "admin", settingSignin, v); err != nil {
		t.Fatal(err)
	}
	if err := f.s.notifyOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	hook.mu.Lock()
	defer hook.mu.Unlock()
	var got [][]string
	for _, e := range hook.events {
		if e.Event == evSettings {
			got = append(got, e.Settings)
			if strings.Contains(e.Text, "idp.lab.test") || strings.Contains(e.Subject, "90d") {
				t.Errorf("a value in the message: %s / %s", e.Subject, e.Text)
			}
		}
	}
	if len(got) != 2 || !slices.Equal(got[0], []string{"idle_timeout_min", "oidc.issuer"}) || !slices.Equal(got[1], []string{"password_max_age"}) {
		t.Fatalf("settings events %v", got)
	}
}

// T28 gate (c) in miniature: a 0.1.2 database with channels of M2 event lists is upgraded by the server, and each
// channel gets the new events of the categories its list covered: capacity_short (jobs) and settings_changed
// (security, which schema 11 gave every channel), not those of categories it did not cover.
func TestUpgradedM2ChannelsGetNewEvents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "etc", "server.json")
	if _, err := Init(ctx, InitOptions{ConfigPath: cfgPath, DataDir: filepath.Join(dir, "data"), AdminUser: "admin", AdminPassword: adminPassword,
		KitPassphrase: kitPassphrase, PublicURL: testPublicURL}); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.resolve(); err != nil {
		t.Fatal(err)
	}
	// The database as 0.1.2 left it (schema 10), with two channels.
	for _, p := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(cfg.dbPath() + p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	old, err := store.Open(cfg.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Migrate(ctx, 10); err != nil {
		t.Fatal(err)
	}
	sinks := map[string]*webhookSink{}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for name, events := range map[string]string{"jobs-hook": `["job_failed"]`, "status-hook": `["status","daily_report"]`} {
		w := newWebhookSink(t)
		w.secret = "secret of " + name
		sinks[name] = w
		conf, _ := json.Marshal(WebhookConfig{URL: w.srv.URL + "/gaia", CAPEM: w.caPEM()})
		if _, err := old.SQL().ExecContext(ctx, `INSERT INTO notification_channels (name, kind, config, events, enabled, cursor, created_at, updated_at)
			VALUES (?, 'webhook', ?, ?, 1, 0, ?, ?)`, name, string(conf), events, now, now); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfg.notifySecretFile(name), []byte(w.secret), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := New(ctx, cfg, cfgPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for name, want := range map[string][]string{"jobs-hook": {catJobs, catSecurity}, "status-hook": {catStatus, catSecurity}} {
		if c, err := s.db.ChannelByName(ctx, name); err != nil || !slices.Equal(c.Categories, want) || c.DailyReport != (name == "status-hook") {
			t.Fatalf("%s after the upgrade: %+v %v", name, c, err)
		}
	}
	// New events of 0.2.0: the proof capacity runs short (jobs) and a setting changes (security).
	if err := s.db.RecordState(ctx, capacityEntity, 0, "", capacityShort, "coverage 80 %, 2 slots needed", "system"); err != nil {
		t.Fatal(err)
	}
	v := defaultSettings()
	v.IdleTimeoutMin = 20
	if err := s.db.SetSetting(ctx, "admin", settingSignin, v); err != nil {
		t.Fatal(err)
	}
	if err := s.notifyOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{"jobs-hook": {evCapacityShort, evSettings}, "status-hook": {evSettings}} {
		w := sinks[name]
		w.mu.Lock()
		var got []string
		for _, e := range w.events {
			if e.Event != evDaily { // a report may fall due
				got = append(got, e.Event)
			}
		}
		bad := w.bad
		w.mu.Unlock()
		if slices.Sort(got); !slices.Equal(got, want) || len(bad) > 0 {
			t.Errorf("%s got %v (problems %v), want %v", name, got, bad, want)
		}
	}
}
