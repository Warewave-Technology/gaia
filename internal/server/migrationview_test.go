package server

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// The migration view is paged on the server (ADR-007 C18.7, CLAUDE.md rule 15): with 300 checks of 150 VMs, a page
// never has more than limit rows, the cursor walks every check exactly once (newest first), latest=true walks each
// VM's newest check once (worst first), the result filter and the counts agree, one VM's history is its checks, and
// each row's evidence id is its bundle's, looked up for the page's bundles only.
func TestMigrationViewPages(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	db := f.s.db
	src, err := db.CreateMigrationSource(ctx, "admin", store.MigrationSource{Name: "esx", Kind: "vsphere", URL: "https://esx.lab/sdk", Username: "ro"})
	if err != nil {
		t.Fatal(err)
	}
	results := []string{"OK", "WARNING", "CRITICAL", "UNKNOWN"}
	const checks, vms, open = 300, 150, 5
	evidence := map[int64]int64{} // check -> evidence id
	readiness := map[int64]string{}
	for i := range checks {
		mc, err := db.QueueMigrationCheck(ctx, "admin", src.ID, fmt.Sprintf("vm-%03d", i%vms), nil, "test")
		if err != nil {
			t.Fatal(err)
		}
		if i >= checks-open { // still queued: no readiness
			readiness[mc.ID] = store.MigrationResultOpen
			continue
		}
		if _, err := db.SetJobState(ctx, "system", mc.JobID, "running", "test", ""); err != nil {
			t.Fatal(err)
		}
		bundle := ""
		if i%3 == 0 {
			bundle = fmt.Sprintf("mc-%d", mc.ID)
		}
		r := results[(i*7)%len(results)]
		if _, err := db.FinishMigrationCheck(ctx, "system", mc.ID, r, "test", bundle, map[string]any{"checks": []any{}}); err != nil {
			t.Fatal(err)
		}
		if bundle != "" {
			e, err := db.AddMigrationEvidence(ctx, store.EvidenceBundle{BundleID: bundle, LedgerSeq: i + 1, BundleSHA256: "00", Verdict: r}, mc.ID)
			if err != nil {
				t.Fatal(err)
			}
			evidence[mc.ID] = e.ID
		}
		if _, err := db.SetJobState(ctx, "system", mc.JobID, "ok", "test", ""); err != nil {
			t.Fatal(err)
		}
		readiness[mc.ID] = r
	}
	tok := f.tokens[store.RoleAuditor]
	walk := func(query string, limit int) ([]MigrationCheckView, MigrationView) {
		t.Helper()
		var all []MigrationCheckView
		var first MigrationView
		after, pages := "", 0
		for {
			var v MigrationView
			q := query + fmt.Sprintf("&limit=%d", limit)
			if after != "" {
				q += "&after=" + url.QueryEscape(after)
			}
			if st := f.do(tok, "GET", "/api/v1/views/migration?"+q, "", &v); st != 200 {
				t.Fatalf("GET %s: %d", q, st)
			}
			if len(v.Checks) > limit {
				t.Fatalf("%s: a page of %d rows, limit %d", q, len(v.Checks), limit)
			}
			if pages == 0 {
				first = v
			}
			pages++
			all = append(all, v.Checks...)
			if v.Next == "" {
				if len(all) != v.Total {
					t.Fatalf("%s: walked %d of %d", query, len(all), v.Total)
				}
				return all, first
			}
			if pages > checks {
				t.Fatalf("%s: the cursor does not end", query)
			}
			after = v.Next
		}
	}

	// Every check, newest first, each once.
	all, v := walk("x=1", 50)
	if v.Total != checks || len(all) != checks || len(v.Checks) != 50 || v.Next != "50" || len(v.Sources) != 1 {
		t.Fatalf("every check: total %d, walked %d, first page %d, next %q", v.Total, len(all), len(v.Checks), v.Next)
	}
	sum := 0
	for _, n := range v.ByResult {
		sum += n
	}
	if sum != checks || v.ByResult[store.MigrationResultOpen] != open {
		t.Fatalf("by result: %+v", v.ByResult)
	}
	ids := map[int64]bool{}
	for i, c := range all {
		if ids[c.ID] || i > 0 && c.ID >= all[i-1].ID {
			t.Fatalf("check %d twice or out of order at %d", c.ID, i)
		}
		ids[c.ID] = true
		want := readiness[c.ID]
		if want == store.MigrationResultOpen {
			want = ""
		}
		if c.Readiness != want || c.Source != "esx" || c.JobState == "" {
			t.Fatalf("check %+v, want readiness %q", c, want)
		}
		if c.EvidenceID != evidence[c.ID] || (c.BundleID != "") != (evidence[c.ID] != 0) {
			t.Fatalf("check %d: evidence %d, want %d (bundle %q)", c.ID, c.EvidenceID, evidence[c.ID], c.BundleID)
		}
		if c.Readiness != "" && c.Result == nil || c.Readiness == "" && (c.Result != nil || c.JobState != "queued") {
			t.Fatalf("check %d: result %+v, job %s", c.ID, c.Result, c.JobState)
		}
	}

	// Each VM's newest check: one row per VM, worst first, then by name.
	rank := map[string]int{"CRITICAL": 0, "WARNING": 1, "UNKNOWN": 2, "": 3, "OK": 4}
	latest, lv := walk("latest=true", 40)
	if lv.Total != vms || len(latest) != vms {
		t.Fatalf("latest: total %d, walked %d", lv.Total, len(latest))
	}
	seen := map[string]bool{}
	for i, c := range latest {
		if seen[c.VM] {
			t.Fatalf("VM %s twice", c.VM)
		}
		seen[c.VM] = true
		if c.ID < int64(checks-vms) { // the second round of checks is each VM's newest
			t.Fatalf("VM %s: check %d is not its newest", c.VM, c.ID)
		}
		if i > 0 {
			p := latest[i-1]
			if rank[c.Readiness] < rank[p.Readiness] || rank[c.Readiness] == rank[p.Readiness] && c.VM < p.VM {
				t.Fatalf("not worst first at %d: %s %s after %s %s", i, c.VM, c.Readiness, p.VM, p.Readiness)
			}
		}
	}

	// The result filter: the rows and the total are the count it had.
	crit, cv := walk("latest=true&result=CRITICAL", 25)
	if cv.Total != lv.ByResult["CRITICAL"] || len(crit) == 0 || slices.ContainsFunc(crit, func(c MigrationCheckView) bool { return c.Readiness != "CRITICAL" }) {
		t.Fatalf("CRITICAL only: total %d, counts %+v", cv.Total, lv.ByResult)
	}
	if cv.ByResult["OK"] != lv.ByResult["OK"] {
		t.Fatalf("the counts do not follow the result filter: %+v / %+v", cv.ByResult, lv.ByResult)
	}
	queued, _ := walk("result=open", 25)
	if len(queued) != open {
		t.Fatalf("open: %d", len(queued))
	}

	// One VM's checks: its history, newest first.
	hist, _ := walk(fmt.Sprintf("source_id=%d&vm=vm-007", src.ID), 10)
	if len(hist) != 2 || hist[0].ID <= hist[1].ID || hist[0].VM != "vm-007" || hist[1].VM != "vm-007" {
		t.Fatalf("history: %+v", hist)
	}

	for _, q := range []string{"limit=201", "result=ready", "after=-1", "source_id=x"} {
		if st := f.do(tok, "GET", "/api/v1/views/migration?"+q, "", nil); st != 400 {
			t.Fatalf("%s: %d", q, st)
		}
	}

	// GET /api/v1/migration-checks is paged with before= (ADR-007 C19.4): the pages walk every check once, newest first.
	var got []int64
	before := int64(0)
	for {
		var page []store.MigrationCheck
		if st := f.do(tok, "GET", fmt.Sprintf("/api/v1/migration-checks?source_id=%d&limit=64&before=%d", src.ID, before), "", &page); st != 200 {
			t.Fatalf("migration-checks before=%d: %d", before, st)
		}
		if len(page) > 64 {
			t.Fatalf("a page of %d checks", len(page))
		}
		if len(page) == 0 {
			break
		}
		for _, c := range page {
			got = append(got, c.ID)
		}
		before = page[len(page)-1].ID
	}
	for i := 1; i < len(got); i++ {
		if got[i] >= got[i-1] {
			t.Fatalf("migration-checks pages: %d after %d", got[i], got[i-1])
		}
	}
	if len(got) != checks {
		t.Fatalf("migration-checks pages: %d checks, want %d", len(got), checks)
	}
}
