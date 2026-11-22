package server

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// fleetFixture is a small fleet with known causes, stored in the status table as the status model computes them:
// kvm-a does not answer (3 VMs), two VMs failed to boot (one on each host), one is never proven, four are OK.
func fleetFixture(t *testing.T) (*fixture, map[string]int64, time.Time) {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	now := time.Now().UTC().Truncate(time.Second) // sessions are real: a far clock would end them
	f.s.now = func() time.Time { return now }
	f.s.db.SetClock(func() time.Time { return now })
	pol := f.ids["policies"]
	ids := map[string]int64{}
	hosts := map[string]int64{}
	for _, h := range []string{"kvm-a", "kvm-b"} {
		hh, err := f.s.db.CreateHost(ctx, store.HostInput{Name: h, Address: h + ".lab", SSHUser: "gaia"})
		if err != nil {
			t.Fatal(err)
		}
		hosts[h] = hh.ID
	}
	targets := &status.Targets{RPOS: 3600, RTOS: 900, MaxProofAgeS: 86400}
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	good := func(id int64) *status.Test {
		rto := 60.0
		return &status.Test{ID: id, PointID: id, Verdict: status.OK, PointTakenAt: ago(30), FinishedAt: ago(20), ProvenRTOS: &rto}
	}
	type vmCase struct {
		name, host string
		in         func(id int64) status.Input
	}
	cases := []vmCase{}
	for i := 1; i <= 3; i++ {
		cases = append(cases, vmCase{fmt.Sprintf("db-%d", i), "kvm-a", func(id int64) status.Input {
			return status.Input{Targets: targets, NewestChecked: &status.Point{ID: id, TakenAt: ago(200 + i)}, LatestTest: good(id), LatestPassingTest: good(id),
				Host:         &status.Scope{Kind: status.ScopeHost, ID: hosts["kvm-a"], Name: "kvm-a"},
				FailedBackup: &status.Backup{JobID: 900 + id, FinishedAt: ago(5), Error: "dial tcp: i/o timeout", Unreachable: true}}
		}})
	}
	for _, h := range []string{"kvm-a", "kvm-b"} {
		cases = append(cases, vmCase{"app-" + h, h, func(id int64) status.Input {
			return status.Input{Targets: targets, NewestChecked: &status.Point{ID: id, TakenAt: ago(10)}, LatestPassingTest: good(id),
				LatestTest: &status.Test{ID: id + 100, PointID: id, Verdict: status.Critical, FailedCheck: "boot", Detail: "boot CRITICAL: no login"}}
		}})
	}
	cases = append(cases, vmCase{"new-1", "kvm-b", func(id int64) status.Input {
		return status.Input{Targets: targets, NewestChecked: &status.Point{ID: id, TakenAt: ago(10)}}
	}})
	for i := 1; i <= 4; i++ {
		cases = append(cases, vmCase{fmt.Sprintf("web-%d", i), "kvm-b", func(id int64) status.Input {
			return status.Input{Targets: targets, NewestChecked: &status.Point{ID: id, TakenAt: ago(10)}, LatestTest: good(id), LatestPassingTest: good(id)}
		}})
	}
	for _, c := range cases {
		v, err := f.s.db.CreateVM(ctx, "admin", store.VMInput{HostID: hosts[c.host], Name: c.name, PolicyID: &pol})
		if err != nil {
			t.Fatal(err)
		}
		in := c.in(v.ID)
		in.Now = now
		r := status.Compute(in)
		if _, err := f.s.db.SetVMStatus(ctx, v.ID, r.Status, r.Reasons[0].Text, r, statusRow(in, r)); err != nil {
			t.Fatal(err)
		}
		ids[c.name] = v.ID
	}
	// The seed VM: computed by the server itself.
	if _, err := f.s.refreshStatus(ctx, f.ids["vms"]); err != nil {
		t.Fatal(err)
	}
	return f, ids, now
}

func TestFleetViews(t *testing.T) {
	f, ids, _ := fleetFixture(t)
	tok := f.tokens[store.RoleAuditor]
	get := func(path string, v any) {
		t.Helper()
		if st := f.do(tok, "GET", path, "", v); st != 200 {
			t.Fatalf("GET %s: %d", path, st)
		}
	}

	var fl FleetView
	get("/api/v1/views/fleet", &fl)
	if fl.VMs != 11 || fl.CanReturn != 4 || fl.ByStatus["CRITICAL"] != 5 || fl.WorstRPO == nil || fl.CodesVer != status.CodesVersion {
		t.Fatalf("fleet: %+v", fl)
	}

	// One host that does not answer is one cause with its three VMs; the boot failures are one cause on two hosts.
	var at AttentionView
	get("/api/v1/views/attention", &at)
	byCode := map[string]AttentionGroup{}
	for _, g := range at.Groups {
		byCode[g.Code] = g
	}
	hu, boot := byCode["host_unreachable"], byCode["test_failed_boot"]
	if hu.Count != 3 || hu.Scope == nil || hu.Scope.Name != "kvm-a" || hu.Status != "CRITICAL" || !strings.Contains(hu.Example.Reason, "host kvm-a does not answer") {
		t.Fatalf("host_unreachable group: %+v", hu)
	}
	if boot.Count != 2 || boot.Scope != nil || len(boot.Hosts) != 2 || boot.Cause != "Restore test failed to boot" {
		t.Fatalf("test_failed_boot group: %+v", boot)
	}
	if at.VMs != fl.Cannot {
		t.Fatalf("attention counts %d VMs, the fleet %d that cannot return", at.VMs, fl.Cannot)
	}
	var p1, p2 AttentionView
	get("/api/v1/views/attention?limit=1", &p1)
	get("/api/v1/views/attention?limit=1&after="+p1.Next, &p2)
	if len(p1.Groups) != 1 || p1.Next == "" || len(p2.Groups) != 1 || p2.Groups[0].Code == p1.Groups[0].Code {
		t.Fatalf("attention pages: %+v / %+v", p1, p2)
	}

	// Coverage by host: kvm-a first (CRITICAL), healthy hosts left out and counted.
	var cov CoverageView
	get("/api/v1/views/coverage?by=host", &cov)
	if len(cov.Groups) == 0 || cov.Groups[0].Name != "kvm-a" || cov.Groups[0].ByStatus["CRITICAL"] != 4 {
		t.Fatalf("coverage by host: %+v", cov)
	}

	// The VM list: worst first; filtered by code and scope; searched; paged with the cursor.
	var all VMListView
	get("/api/v1/views/vm-list?limit=200", &all)
	if all.Total != 11 || len(all.Rows) != 11 || all.Rows[0].Status != "CRITICAL" || all.Rows[len(all.Rows)-1].Status != "OK" {
		t.Fatalf("vm list: %d %+v", all.Total, all.Rows)
	}
	var seen []string
	next := ""
	for page := 0; page < 20; page++ {
		var pg VMListView
		get("/api/v1/views/vm-list?limit=3&after="+url.QueryEscape(next), &pg)
		for _, r := range pg.Rows {
			seen = append(seen, r.VM)
		}
		if next = pg.Next; next == "" {
			break
		}
	}
	if len(seen) != 11 {
		t.Fatalf("pages gave %v", seen)
	}
	for i, r := range all.Rows {
		if seen[i] != r.VM {
			t.Fatalf("pages %v, one page %v", seen, all.Rows)
		}
	}
	var hu1 VMListView
	get(fmt.Sprintf("/api/v1/views/vm-list?code=host_unreachable&scope_kind=host&scope_id=%d", hu.Scope.ID), &hu1)
	if hu1.Total != 3 || hu1.Rows[0].Code != "host_unreachable" || hu1.Rows[0].ProvenRPOS == nil || hu1.Rows[0].Targets == nil {
		t.Fatalf("host_unreachable VMs: %+v", hu1)
	}
	// The deciding reason's params come with the row (ADR-007 C11), the same as the reason's own.
	if p := hu1.Rows[0].Params; p.Job == 0 || p.JobAt == nil || p.PointAt == nil || p.TargetS != 3600 || p.Detail != "dial tcp: i/o timeout" {
		t.Fatalf("host_unreachable row params: %+v", p)
	}
	var att VMListView
	get("/api/v1/views/vm-list?attention=true", &att)
	if att.Total != fl.Cannot {
		t.Fatalf("attention list %d, fleet cannot %d", att.Total, fl.Cannot)
	}
	var q VMListView
	get("/api/v1/views/vm-list?q=kvm-a", &q)
	if q.Total != 4 {
		t.Fatalf("search kvm-a in the list: %d", q.Total)
	}

	// Tags: set by a writer, refused for an auditor, filtered and grouped.
	if st := f.do(tok, "PUT", fmt.Sprintf("/api/v1/vms/%d/tags", ids["db-1"]), `{"tags":["env:prod"]}`, nil); st != 403 {
		t.Fatalf("an auditor sets tags: %d", st)
	}
	op := f.tokens[store.RoleOperator]
	for _, n := range []string{"db-1", "db-2", "web-1"} {
		var tags []string
		if st := f.do(op, "PUT", fmt.Sprintf("/api/v1/vms/%d/tags", ids[n]), `{"tags":["env:prod","pci"]}`, &tags); st != 200 || len(tags) != 2 {
			t.Fatalf("tags of %s: %d %v", n, st, tags)
		}
	}
	if st := f.do(op, "PUT", fmt.Sprintf("/api/v1/vms/%d/tags", ids["db-1"]), `{"tags":["bad key"]}`, nil); st != 400 {
		t.Fatalf("a bad tag: %d", st)
	}
	var tagged VMListView
	get("/api/v1/views/vm-list?tag=env:prod", &tagged)
	if tagged.Total != 3 || len(tagged.Rows[0].Tags) != 2 {
		t.Fatalf("by tag: %+v", tagged)
	}
	var byTag CoverageView
	get("/api/v1/views/coverage?by=tag&all=true", &byTag)
	if len(byTag.Groups) != 2 || byTag.Groups[0].VMs != 3 {
		t.Fatalf("coverage by tag: %+v", byTag)
	}

	// Search: names and a point id.
	var sr SearchView
	get("/api/v1/search?q=web", &sr)
	if len(sr.VMs) != 4 {
		t.Fatalf("search web: %+v", sr)
	}
	pts, _ := f.s.db.Points(context.Background(), f.ids["vms"], 1)
	get(fmt.Sprintf("/api/v1/search?q=%d", pts[0].ID), &sr)
	if len(sr.Points) != 1 || sr.Points[0].VM != "seed-vm" {
		t.Fatalf("search a point id: %+v", sr)
	}

	var codes ReasonCodes
	get("/api/v1/reason-codes", &codes)
	if codes.Version != status.CodesVersion || len(codes.Codes) != len(status.Defs()) {
		t.Fatalf("reason codes: %+v", codes)
	}
}

// The status table and the status model agree after events and the clock; a VM whose next change has passed is due
// until the next tick, and the tick makes it equal again.
func TestStatusTableCheck(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	clock := time.Date(2026, 11, 16, 12, 30, 0, 0, time.UTC)
	f.s.now = func() time.Time { return clock }
	f.s.db.SetClock(func() time.Time { return clock })
	pts, _ := f.s.db.Points(ctx, f.ids["vms"], 1)
	if _, err := f.s.db.SetPointCheck(ctx, "system", pts[0].ID, true, "seed"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.refreshStatuses(ctx, clock); err != nil { // the first tick recomputes everything
		t.Fatal(err)
	}
	var chk StatusCheck
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/status/check", "", &chk); st != 200 || chk.Verdict != "OK" || chk.Equal != chk.VMs || chk.VMs != 1 {
		t.Fatalf("check after the first tick: %d %+v", st, chk)
	}
	rows, _ := f.s.db.VMStatuses(ctx, f.ids["vms"])
	if rows[0].NextChangeAt == nil {
		t.Fatalf("no next change for a VM with a checked point and targets: %+v", rows[0])
	}
	// Past the RPO target: the VM is due, and the tick (not the full recompute) brings it to CRITICAL.
	clock = timeOf(*rows[0].NextChangeAt).Add(time.Second)
	if chk, _ = f.s.checkStatuses(ctx); chk.Due != 1 || len(chk.Differ) != 0 {
		t.Fatalf("check when due: %+v", chk)
	}
	if err := f.s.refreshStatuses(ctx, clock); err != nil {
		t.Fatal(err)
	}
	if chk, _ = f.s.checkStatuses(ctx); chk.Verdict != "OK" || chk.Equal != 1 {
		t.Fatalf("check after the tick: %+v", chk)
	}
	if rows, _ = f.s.db.VMStatuses(ctx, f.ids["vms"]); rows[0].Status != "CRITICAL" || rows[0].Code != string(status.NoBackupInRPO) {
		t.Fatalf("after the RPO target: %+v", rows[0])
	}
	// A changed policy marks its VMs due.
	if st := f.do(f.tokens[store.RoleAdmin], "PATCH", fmt.Sprintf("/api/v1/policies/%d", f.ids["policies"]), `{"rpo_target_s":86400}`, nil); st != 200 {
		t.Fatalf("policy change: %d", st)
	}
	if chk, _ = f.s.checkStatuses(ctx); chk.Due != 1 {
		t.Fatalf("after a policy change: %+v", chk)
	}
	var rc RecomputeStats
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/status/recompute", "", &rc); st != 200 || rc.VMs != 1 || rc.Changed != 1 {
		t.Fatalf("recompute: %d %+v", st, rc)
	}
}
