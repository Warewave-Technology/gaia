package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// capacity_short (ADR-007 C13): once per drop below 100 % coverage, with the slots needed; a notice on the overview
// and a line in the daily report; never a VM status; nothing while the sandbox helper's capacity is unknown.
func TestCapacityShort(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	slots := func(n int) {
		f.s.sandboxCap.mu.Lock()
		f.s.sandboxCap.cap, f.s.sandboxCap.err, f.s.sandboxCap.at = &sandbox.Capacity{Slots: n}, nil, time.Now()
		f.s.sandboxCap.mu.Unlock()
	}
	changes := func() []store.StateChange {
		t.Helper()
		cs, err := f.s.db.StateChanges(ctx, capacityEntity, 1, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	now := time.Now()
	// Unknown (the helper never answered): nothing.
	if err := f.s.watchCapacity(ctx, now); err != nil || len(changes()) != 0 || f.s.capacityNotice() != nil {
		t.Fatalf("unknown capacity: %v %+v", err, changes())
	}
	f.s.capWatch.at = time.Time{}
	slots(0) // no slot: the seed VM's tests cannot run
	if err := f.s.watchCapacity(ctx, now); err != nil {
		t.Fatal(err)
	}
	cs := changes()
	if len(cs) != 1 || cs[0].From != "" || cs[0].To != capacityShort || !strings.Contains(cs[0].Reason, "0 slot(s), 1 needed") {
		t.Fatalf("drop: %+v", cs)
	}
	ev, err := f.s.classify(ctx, cs[0])
	if err != nil || ev == nil || ev.Event != evCapacityShort || !strings.Contains(ev.Subject, "1 needed") {
		t.Fatalf("event: %+v %v", ev, err)
	}
	var fl FleetView
	if code := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/fleet", "", &fl); code != 200 || fl.CapacityShort == nil ||
		fl.CapacityShort.SlotsNeeded != 1 || fl.CapacityShort.Coverage != 0 || fl.CapacityShort.Since != cs[0].At {
		t.Fatalf("overview notice: %d %+v", code, fl.CapacityShort)
	}
	// Still short at the next looks: no new event. Not looked at again within 5 minutes.
	slots(0)
	f.s.watchCapacity(ctx, now.Add(time.Minute))
	f.s.watchCapacity(ctx, now.Add(6*time.Minute))
	if n := len(changes()); n != 1 {
		t.Fatalf("%d changes while short", n)
	}
	// Enough again: recorded, not an event; the notice goes.
	slots(4)
	if err := f.s.watchCapacity(ctx, now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	cs = changes()
	if len(cs) != 2 || cs[1].To != capacityOK || f.s.capacityNotice() != nil {
		t.Fatalf("recovered: %+v", cs)
	}
	if ev, err := f.s.classify(ctx, cs[1]); err != nil || ev != nil {
		t.Fatalf("recovery event: %+v %v", ev, err)
	}
	// A second drop is a second event; the daily report counts both and says it is short.
	slots(0)
	f.s.watchCapacity(ctx, now.Add(18*time.Minute))
	rep, err := f.s.makeDailyReport(ctx, "admin", "test")
	if err != nil {
		t.Fatal(err)
	}
	var doc DailyReportDoc
	if err := json.Unmarshal(rep.Report, &doc); err != nil {
		t.Fatal(err)
	}
	if !doc.Capacity.Short || doc.Capacity.Drops != 2 || !strings.Contains(reportText(doc), "Proof capacity is short since") {
		t.Fatalf("report: %+v\n%s", doc.Capacity, reportText(doc))
	}
	// No VM's status names it.
	if st, _, err := f.s.storedStatus(ctx, f.ids["vms"]); err != nil || strings.Contains(st.Reasons[0].Text, "capacity") {
		t.Fatalf("VM status: %+v %v", st.Reasons, err)
	}
	// Category jobs: a channel that takes job_failed gets it; one that takes only status does not.
	if !channelTakes([]string{evJobFailed}, evCapacityShort) || channelTakes([]string{evStatus}, evCapacityShort) || !channelTakes([]string{evCapacityShort}, evCapacityShort) {
		t.Fatal("channelTakes")
	}
}
