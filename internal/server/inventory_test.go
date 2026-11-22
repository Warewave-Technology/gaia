package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// The Activity and Inventory views (T27): filters, who queued a job, pages, and the hosts' and repositories' rows.
func TestInventoryViews(t *testing.T) {
	ctx := context.Background()
	f, ids, _ := fleetFixture(t)
	tok := f.tokens[store.RoleAuditor]
	get := func(path string, v any) {
		t.Helper()
		if st := f.do(tok, "GET", path, "", v); st != 200 {
			t.Fatalf("GET %s: %d", path, st)
		}
	}
	job := func(actor, kind, vm string, states ...string) store.Job {
		t.Helper()
		id := ids[vm]
		j, err := f.s.db.CreateJob(ctx, actor, kind, &id, "test job")
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range states {
			if j, err = f.s.db.SetJobState(ctx, "system", j.ID, st, "test", map[bool]string{true: "dial tcp: i/o timeout"}[st == "failed"]); err != nil {
				t.Fatal(err)
			}
		}
		return j
	}
	running := job("yigit", "backup", "db-1", "running")
	failed := job("system", "backup", "db-2", "running", "failed")
	queued := job("system", "restore_test", "web-1")
	job("system", "backup", "web-2", "running", "ok")

	var all ActivityView
	get("/api/v1/views/activity", &all)
	if all.Total < 4 || all.Rows[0].ID != f.lastJob(t) || len(all.LastHour) == 0 {
		t.Fatalf("activity: %+v", all)
	}
	byID := map[int64]store.ActivityRow{}
	for _, r := range all.Rows {
		byID[r.ID] = r
	}
	if r := byID[running.ID]; r.By != "yigit" || r.VM != "db-1" || r.Host != "kvm-a" || r.State != "running" {
		t.Fatalf("running job row: %+v", r)
	}
	if r := byID[failed.ID]; r.By != "system" || r.Error == "" {
		t.Fatalf("failed job row: %+v", r)
	}
	open := 0
	for _, c := range all.Open {
		open += c.Count
	}
	var openRows ActivityView
	get("/api/v1/views/activity?state=open&limit=200", &openRows)
	if openRows.Total != open || open < 2 {
		t.Fatalf("open jobs: counted %d, listed %d", open, openRows.Total)
	}
	var tests ActivityView
	get("/api/v1/views/activity?kind=restore_test&state=queued", &tests)
	if tests.Total < 1 || tests.Rows[0].ID != queued.ID {
		t.Fatalf("queued tests: %+v", tests)
	}
	var host ActivityView
	get(fmt.Sprintf("/api/v1/views/activity?host_id=%d", byID[running.ID].HostID), &host)
	for _, r := range host.Rows {
		if r.Host != "kvm-a" {
			t.Fatalf("a job of another host in kvm-a's: %+v", r)
		}
	}
	// Pages: newest first, each job once.
	seen := map[int64]bool{}
	next := ""
	for page := 0; page < 50; page++ {
		var pg ActivityView
		get("/api/v1/views/activity?limit=2&before="+next, &pg)
		for _, r := range pg.Rows {
			if seen[r.ID] {
				t.Fatalf("job %d on two pages", r.ID)
			}
			seen[r.ID] = true
		}
		if next = pg.Next; next == "" {
			break
		}
	}
	if len(seen) != all.Total {
		t.Fatalf("pages gave %d jobs of %d", len(seen), all.Total)
	}
	for _, q := range []string{"kind=nightly", "state=done", "before=x", "limit=201"} {
		if st := f.do(tok, "GET", "/api/v1/views/activity?"+q, "", nil); st != 400 {
			t.Fatalf("activity?%s: %d", q, st)
		}
	}

	// Hosts: kvm-a's three VMs cannot be backed up because it does not answer.
	var hv HostsView
	get("/api/v1/views/hosts", &hv)
	hosts := map[string]store.HostRow{}
	for _, h := range hv.Rows {
		hosts[h.Name] = h
	}
	if a := hosts["kvm-a"]; a.VMs != 4 || a.Unreachable != 3 || a.UnreachSince == "" || a.Critical != 4 {
		t.Fatalf("kvm-a: %+v", a)
	}
	if b := hosts["kvm-b"]; b.VMs != 6 || b.Unreachable != 0 || b.OK != 4 || b.Unknown != 1 {
		t.Fatalf("kvm-b: %+v", b)
	}
	var h1 HostsView
	get("/api/v1/views/hosts?limit=1", &h1)
	if h1.Total != hv.Total || len(h1.Rows) != 1 || h1.Next != "1" {
		t.Fatalf("hosts page: %+v", h1)
	}

	var rv RepositoriesView
	get("/api/v1/views/repositories", &rv)
	if rv.Total != 1 || rv.Rows[0].Policies != 1 || rv.Rows[0].VMs != 11 {
		t.Fatalf("repositories: %+v", rv)
	}

	// The audit log, newest first: an auditor reads it, an operator does not.
	var av AuditView
	get("/api/v1/views/audit?limit=2", &av)
	if av.Total < 3 || len(av.Rows) != 2 || av.Rows[0].Seq <= av.Rows[1].Seq || av.Next == "" {
		t.Fatalf("audit: %+v", av)
	}
	var av2 AuditView
	get("/api/v1/views/audit?limit=2&before="+av.Next, &av2)
	if len(av2.Rows) == 0 || av2.Rows[0].Seq >= av.Rows[1].Seq {
		t.Fatalf("audit page 2: %+v", av2)
	}
	if st := f.do(f.tokens[store.RoleOperator], "GET", "/api/v1/views/audit", "", nil); st != 403 {
		t.Fatalf("an operator reads the audit log: %d", st)
	}

	// The VM page's history strip reaches back 24 h or 7 days when asked.
	var vv VMView
	get(fmt.Sprintf("/api/v1/views/vms/%d?window_h=168", ids["db-1"]), &vv)
	if vv.WindowS != 7*24*3600 {
		t.Fatalf("window_s %d", vv.WindowS)
	}
	if st := f.do(tok, "GET", fmt.Sprintf("/api/v1/views/vms/%d?window_h=12", ids["db-1"]), "", nil); st != 400 {
		t.Fatalf("window_h=12: %d", st)
	}
}

// lastJob is the newest job's id.
func (f *fixture) lastJob(t *testing.T) int64 {
	t.Helper()
	js, err := f.s.db.Jobs(context.Background(), 0, 1)
	if err != nil || len(js) != 1 {
		t.Fatalf("jobs: %v %v", js, err)
	}
	return js[0].ID
}
