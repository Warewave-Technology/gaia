package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// ADR-007 C12: a policy whose test interval is longer than its RPO target (or maximum proof age) says how much of the
// time its VMs can be OK and which interval would make it always possible, in the policy API and in a what-if before
// saving; saving is allowed. Its VMs' OK <-> WARNING changes whose only reason is the proof's age are not notified at
// once, and the daily report counts them.
func TestPolicyMaxOKFraction(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	op := f.tokens[store.RoleOperator]
	body := fmt.Sprintf(`{"name":"daily","repository_id":%d,"backup_interval_s":900,"test_interval_s":86400,"keep_last":8,"keep_daily":7,"keep_weekly":4,"rpo_target_s":14400,"rto_target_s":1800,"max_proof_age_s":172800,"check_template":"linux-basic"}`, f.ids["repositories"])
	var p PolicyView
	if code := f.do(op, "POST", "/api/v1/policies", body, &p); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if math.Abs(p.MaxOKFraction-4.0/24) > 1e-9 || p.SuggestedTestIntervalS == nil || *p.SuggestedTestIntervalS != 14400 {
		t.Fatalf("created: %v %v", p.MaxOKFraction, p.SuggestedTestIntervalS)
	}
	var got PolicyView
	if code := f.do(f.tokens[store.RoleAuditor], "GET", fmt.Sprintf("/api/v1/policies/%d", p.ID), "", &got); code != 200 || got.MaxOKFraction != p.MaxOKFraction {
		t.Fatalf("get: %d %+v", code, got)
	}
	var all []PolicyView
	if code := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/policies", "", &all); code != 200 || len(all) < 2 {
		t.Fatalf("list: %d %+v", code, all)
	}
	for _, x := range all {
		if x.ID != p.ID && (x.MaxOKFraction <= 0 || x.MaxOKFraction > 1) {
			t.Fatalf("policy %s: %v", x.Name, x.MaxOKFraction)
		}
	}
	// What-if, before saving: a shorter test interval makes OK always possible; nothing is saved.
	var w CapacityView
	if code := f.do(op, "POST", "/api/v1/capacity/what-if", fmt.Sprintf(`{"policies":[{"policy_id":%d,"test_interval_s":7200}]}`, p.ID), &w); code != 200 ||
		len(w.Policies) != 1 || w.Policies[0].PolicyID != p.ID || w.Policies[0].MaxOKFraction != 1 || w.Policies[0].SuggestedTestIntervalS != nil {
		t.Fatalf("what-if: %d %+v", code, w.Policies)
	}
	if code := f.do(op, "POST", "/api/v1/capacity/what-if", fmt.Sprintf(`{"policies":[{"policy_id":%d,"rpo_target_s":3600}]}`, p.ID), &w); code != 200 ||
		math.Abs(w.Policies[0].MaxOKFraction-1.0/24) > 1e-9 || *w.Policies[0].SuggestedTestIntervalS != 3600 {
		t.Fatalf("what-if rpo: %d %+v", code, w.Policies)
	}
	if again, _ := f.s.db.Policy(ctx, p.ID); again.TestIntervalS != 86400 || again.RPOTargetS != 14400 {
		t.Fatalf("the what-if saved: %+v", again)
	}

	// The notification rule.
	host := f.ids["hosts"]
	vm, err := f.s.db.CreateVM(ctx, "admin", store.VMInput{HostID: host, Name: "daily-vm", PolicyID: &p.ID})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	targets := &status.Targets{RPOS: 14400, RTOS: 1800, MaxProofAgeS: 172800}
	set := func(provenAge time.Duration, rto float64) {
		t.Helper()
		test := &status.Test{ID: 1, PointID: 1, Verdict: status.OK, PointTakenAt: now.Add(-provenAge), FinishedAt: now.Add(-provenAge), ProvenRTOS: &rto}
		in := status.Input{Now: now, Targets: targets, NewestChecked: &status.Point{ID: 2, TakenAt: now.Add(-time.Minute)}, LatestTest: test, LatestPassingTest: test}
		r := status.Compute(in)
		if _, err := f.s.db.SetVMStatus(ctx, vm.ID, r.Status, r.Reasons[0].Text, r, statusRow(in, r)); err != nil {
			t.Fatal(err)
		}
	}
	set(time.Hour, 60)    // (new) -> OK: sent
	set(5*time.Hour, 60)  // OK -> WARNING, proven_rpo_over_target only: quiet
	set(time.Hour, 60)    // WARNING -> OK: quiet
	set(5*time.Hour, 900) // OK -> WARNING, proven_rpo_over_target only (RTO 900 s is within 1800 s): quiet
	chs, err := f.s.db.StateChanges(ctx, "vm_status", vm.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	quiet, loud := 0, 0
	for _, sc := range chs {
		ev, err := f.s.classify(ctx, sc)
		if err != nil {
			t.Fatal(err)
		}
		if ev == nil {
			quiet++
			if !(sc.From == status.OK && sc.To == status.Warning || sc.From == status.Warning && sc.To == status.OK) {
				t.Fatalf("change %+v kept quiet", sc)
			}
		} else {
			loud++
		}
	}
	if quiet != 3 || loud != 1 {
		t.Fatalf("quiet %d, sent %d: %+v", quiet, loud, chs)
	}
	// A WARNING with its own RTO reason after an OK is sent.
	set(time.Hour, 60)
	set(time.Hour, 3000)
	chs, _ = f.s.db.StateChanges(ctx, "vm_status", vm.ID, chs[len(chs)-1].Seq, 100)
	if len(chs) != 2 {
		t.Fatalf("changes %+v", chs)
	}
	if ev, err := f.s.classify(ctx, chs[0]); err != nil || ev != nil {
		t.Fatalf("WARNING (proof age) -> OK: %+v %v", ev, err)
	}
	if ev, err := f.s.classify(ctx, chs[1]); err != nil || ev == nil || ev.Code != string(status.RTOOverTarget) {
		t.Fatalf("OK -> WARNING (RTO): %+v %v", ev, err)
	}
	q, err := f.s.quietChanges(ctx, "")
	if err != nil || q.Changes != 4 || q.VMs != 1 || len(q.Policies) != 1 || q.Policies[0].Policy != "daily" {
		t.Fatalf("quiet changes: %+v %v", q, err)
	}
	rep, err := f.s.makeDailyReport(ctx, "admin", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rep.Report), `"quiet_status_changes"`) {
		t.Fatalf("report: %s", rep.Report)
	}
	var doc DailyReportDoc
	if err := json.Unmarshal(rep.Report, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.QuietChanges.Changes != 4 || !strings.Contains(reportText(doc), "Not sent at once: 4 OK <-> WARNING changes of 1 VM(s)") {
		t.Fatalf("report quiet: %+v\n%s", doc.QuietChanges, reportText(doc))
	}
}
