package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
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

// ADR-007 C19.6: an export given without a ledger record (C18.5) is a row of the Evidence screen, newest first at the
// top of the first page, saying not_recorded (no entry, no hash, UNKNOWN), and the view counts them whatever the
// filter; the rows stay within a page and the pages after the first are the ledger's. Once the next ledger write
// appends the records, they are ledger entries that say they were recorded late.
func TestEvidenceViewNotRecorded(t *testing.T) {
	f := newFixture(t)
	f.seed()
	good := damageLedger(t, f)
	_, h1 := f.exportPath(store.RoleAuditor, "/api/v1/evidence-export")
	_, h2 := f.exportPath(store.RoleAdmin, "/api/v1/evidence-export")
	p1, p2 := h1.Get("X-Gaia-Export-Pending-Record"), h2.Get("X-Gaia-Export-Pending-Record")
	if p1 == "" || p2 == "" {
		t.Fatalf("pending records %q %q", p1, p2)
	}
	get := func(q string) EvidenceView {
		t.Helper()
		var v EvidenceView
		if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/evidence"+q, "", &v); st != http.StatusOK {
			t.Fatalf("%s: %d", q, st)
		}
		return v
	}
	v := get("?limit=25")
	if v.Total != 3 || v.NotRecorded != 2 || len(v.Rows) != 3 || v.Next != "" {
		t.Fatalf("all: total %d, not recorded %d, %d rows, next %q", v.Total, v.NotRecorded, len(v.Rows), v.Next)
	}
	for i, want := range []string{p2, p1} {
		r := v.Rows[i]
		if r.BundleID != want || r.LedgerRecord != ledgerNotRecorded || r.LedgerSeq != 0 || r.ID != 0 || r.BundleSHA256 != "" || r.Verdict != "UNKNOWN" ||
			r.Kind != "export" || r.By == "" || !strings.Contains(r.NotRecordedReason, "not intact") || r.CreatedAt == "" {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	if r := v.Rows[2]; r.LedgerRecord != ledgerRecorded || r.LedgerSeq == 0 || r.BundleID != "seed-vm-test-1" {
		t.Fatalf("the ledger's row: %+v", r)
	}
	// A page full of them: the ledger's entries follow on the next page, without a gap.
	v = get("?limit=2")
	if len(v.Rows) != 2 || v.Rows[0].BundleID != p2 || v.Rows[1].BundleID != p1 || v.Next == "" {
		t.Fatalf("limit 2: %+v next %q", v.Rows, v.Next)
	}
	if n := get("?limit=2&before=" + v.Next); len(n.Rows) != 1 || n.Rows[0].BundleID != "seed-vm-test-1" || n.Next != "" || n.Total != 3 {
		t.Fatalf("page 2: %+v next %q total %d", n.Rows, n.Next, n.Total)
	}
	if v = get("?limit=1"); len(v.Rows) != 1 || v.Rows[0].BundleID != p2 || v.Next == "" || v.NotRecorded != 2 {
		t.Fatalf("limit 1: %+v next %q", v.Rows, v.Next)
	}
	// Filters: restore tests and failures have none of them; the count is there all the same.
	for _, q := range []string{"?kind=test", "?verdict=CRITICAL", "?kind=migration"} {
		if v := get(q); v.NotRecorded != 2 || slices.ContainsFunc(v.Rows, func(r EvidenceRow) bool { return r.LedgerRecord == ledgerNotRecorded }) {
			t.Fatalf("%s: %+v", q, v)
		}
	}
	if v := get("?kind=export"); v.Total != 2 || len(v.Rows) != 2 {
		t.Fatalf("exports: %+v", v)
	}
	if v := get("?verdict=UNKNOWN"); v.Total != 2 || len(v.Rows) != 2 {
		t.Fatalf("UNKNOWN: %+v", v)
	}

	// Repaired, "Check now" appends both: ledger entries that say they were late, nothing not recorded.
	if err := os.WriteFile(f.s.cfg.ledgerPath(), good, 0o644); err != nil {
		t.Fatal(err)
	}
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/ledger/verify", "", nil); st != http.StatusOK {
		t.Fatalf("check now: %d", st)
	}
	v = get("?limit=25")
	if v.Total != 3 || v.NotRecorded != 0 || len(v.Rows) != 3 {
		t.Fatalf("after the append: %+v", v)
	}
	for i, want := range []string{p2, p1} {
		if r := v.Rows[i]; r.BundleID != want || r.LedgerRecord != ledgerRecordedLate || r.LedgerSeq == 0 || r.ID == 0 || r.BundleSHA256 == "" || r.NotRecordedReason != "" {
			t.Fatalf("row %d after the append: %+v", i, r)
		}
	}
}
