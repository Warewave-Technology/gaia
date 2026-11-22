package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// The Evidence screen's entries come one page at a time, newest first, with the name of what each proves; the pages
// follow each other without a gap or a repeat, the filters narrow the count and the rows, and every role may read them.
func TestEvidenceViewPages(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed() // one sealed bundle of seed-vm's restore test
	vms, err := f.s.db.VMs(ctx, 0)
	if err != nil || len(vms) == 0 {
		t.Fatalf("seed VMs: %v %v", vms, err)
	}
	vm := vms[0]
	// 60 more entries: every third one failed.
	for i := 1; i <= 60; i++ {
		verdict := "OK"
		if i%3 == 0 {
			verdict = "CRITICAL"
		}
		if _, err := f.s.db.AddEvidence(ctx, store.EvidenceBundle{BundleID: fmt.Sprintf("b-%d", i), VMID: &vm.ID, LedgerSeq: 1 + i, BundleSHA256: "00", Verdict: verdict}); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{store.RoleAdmin, store.RoleOperator, store.RoleAuditor} {
		var v EvidenceView
		if st := f.do(f.tokens[role], "GET", "/api/v1/views/evidence?limit=5", "", &v); st != http.StatusOK {
			t.Fatalf("%s: %d", role, st)
		}
	}
	if st := f.do("", "GET", "/api/v1/views/evidence", "", nil); st != http.StatusUnauthorized {
		t.Fatalf("no session: %d, want 401", st)
	}

	seen := map[int64]bool{}
	cursor, pages, last := "", 0, int64(1<<62)
	for {
		var v EvidenceView
		q := "/api/v1/views/evidence?limit=25"
		if cursor != "" {
			q += "&before=" + cursor
		}
		if st := f.do(f.tokens[store.RoleAuditor], "GET", q, "", &v); st != http.StatusOK {
			t.Fatalf("page %d: %d", pages, st)
		}
		pages++
		if v.Total != 61 {
			t.Fatalf("total %d, want 61", v.Total)
		}
		if len(v.Rows) > 25 || len(v.Rows) == 0 {
			t.Fatalf("page %d has %d rows", pages, len(v.Rows))
		}
		for _, r := range v.Rows {
			if r.ID >= last || seen[r.ID] {
				t.Fatalf("row %d out of order or repeated", r.ID)
			}
			last = r.ID
			seen[r.ID] = true
			if r.VM != vm.Name {
				t.Fatalf("row %d names %q, want %q", r.ID, r.VM, vm.Name)
			}
		}
		if v.Next == "" {
			break
		}
		cursor = v.Next
	}
	if pages != 3 || len(seen) != 61 {
		t.Fatalf("%d pages, %d rows; want 3 pages of 61 rows", pages, len(seen))
	}

	var bad EvidenceView
	if st := f.do(f.tokens[store.RoleAdmin], "GET", "/api/v1/views/evidence?verdict=CRITICAL&kind=test&limit=200", "", &bad); st != http.StatusOK {
		t.Fatalf("filtered: %d", st)
	}
	if bad.Total != 20 || len(bad.Rows) != 20 || bad.Next != "" {
		t.Fatalf("CRITICAL: total %d, %d rows, next %q; want 20, 20, none", bad.Total, len(bad.Rows), bad.Next)
	}
	for _, r := range bad.Rows {
		if r.Verdict != "CRITICAL" {
			t.Fatalf("row %d is %s", r.ID, r.Verdict)
		}
	}
	// No migration check sealed anything.
	var mig EvidenceView
	if st := f.do(f.tokens[store.RoleAdmin], "GET", "/api/v1/views/evidence?kind=migration", "", &mig); st != http.StatusOK || mig.Total != 0 || len(mig.Rows) != 0 {
		t.Fatalf("migration entries: %d, total %d", st, mig.Total)
	}
	for _, q := range []string{"kind=other", "verdict=ok", "limit=201", "before=-1"} {
		if st := f.do(f.tokens[store.RoleAdmin], "GET", "/api/v1/views/evidence?"+q, "", nil); st != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", q, st)
		}
	}
}
