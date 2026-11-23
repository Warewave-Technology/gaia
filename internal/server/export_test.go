package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// untarExport unpacks an export into a new directory and returns its root (gaia-evidence-<time>).
func untarExport(t *testing.T, b []byte) string {
	t.Helper()
	dir := t.TempDir()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	root := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		root = strings.SplitN(h.Name, "/", 2)[0]
		p := filepath.Join(dir, h.Name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, root)
}

// export makes an export as a role through the API (with its step-up) and returns the tar.gz and the recorded bundle.
func (f *fixture) export(role string) ([]byte, string) {
	f.t.Helper()
	tok := f.tokens[role]
	var b []byte
	h := f.stepUp(tok, "GET", "/api/v1/evidence-export")
	req, _ := http.NewRequest("GET", f.ts.URL+"/api/v1/evidence-export", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("export as %s: %d %s", role, resp.StatusCode, b)
	}
	return b, resp.Header.Get("X-Gaia-Export-Bundle")
}

// TestT28ExportFixture is a fixture for the T28 gate (b) on any machine (lab/lab-vm/t28-export.sh local), skipped
// unless GAIA_T28_OUT is set: a test server with a sealed restore test bundle and a daily report makes two exports
// through the API (an auditor's, then an admin's, each with its step-up), checks the ledger ("Check now"), and writes
// into GAIA_T28_OUT what the gate script reads, as the lab's CLI writes it: system.json, daily-reports.json,
// export-N.tar.gz, ledger-check.json, evidence-list.json.
func TestT28ExportFixture(t *testing.T) {
	out := os.Getenv("GAIA_T28_OUT")
	if out == "" {
		t.Skip("fixture: set GAIA_T28_OUT (lab/lab-vm/t28-export.sh local)")
	}
	ctx := context.Background()
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		t.Helper()
		b, ok := v.([]byte)
		if !ok {
			var err error
			if b, err = json.MarshalIndent(v, "", "  "); err != nil {
				t.Fatal(err)
			}
			b = append(b, '\n')
		}
		if err := os.WriteFile(filepath.Join(out, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := newFixture(t)
	f.seed()
	if _, err := f.s.makeDailyReport(ctx, "admin", "t28 fixture"); err != nil {
		t.Fatal(err)
	}
	var sys map[string]any
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/system", "", &sys); st != 200 {
		t.Fatalf("system: %d", st)
	}
	write("system.json", sys)
	var reps []json.RawMessage
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/daily-reports?limit=1", "", &reps); st != 200 {
		t.Fatalf("daily reports: %d", st)
	}
	write("daily-reports.json", reps)
	for i, role := range []string{store.RoleAuditor, store.RoleAdmin} {
		b, id := f.export(role)
		write(fmt.Sprintf("export-%d.tar.gz", i+1), b)
		write(fmt.Sprintf("export-%d.json", i+1), map[string]any{"command": "evidence", "file": fmt.Sprintf("export-%d.tar.gz", i+1), "bytes": len(b), "recorded_as": id})
	}
	var c store.LedgerCheck
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/ledger/verify", "", &c); st != 200 {
		t.Fatalf("ledger check: %d", st)
	}
	write("ledger-check.json", c)
	var ev []store.EvidenceBundle
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/evidence?limit=10", "", &ev); st != 200 {
		t.Fatalf("evidence: %d", st)
	}
	write("evidence-list.json", ev)
}

// ADR-007 C7: the export's SHA256SUMS is signed by the evidence key, and every export is recorded as an evidence
// bundle (who, scope, period, the SUMS hash) with its ledger entry, which v1 verification checks unchanged and the
// next export contains; the ledger checks (hourly incremental, daily full: C14) stay OK.
func TestExportSignedAndRecorded(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	if _, err := f.s.makeDailyReport(ctx, "admin", "test"); err != nil {
		t.Fatal(err)
	}
	if c, err := f.s.checkLedger(ctx, "system", "before", store.LedgerCheckFull); err != nil || c.Verdict != evidence.OK {
		t.Fatalf("ledger check before the export: %+v %v", c, err)
	}
	pub, err := evidence.LoadPublicKey(f.s.cfg.pubPath())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.s.db.EvidenceBundles(ctx, 0, 100)

	b1, id1 := f.export(store.RoleAuditor)
	x1 := untarExport(t, b1)
	if !strings.HasPrefix(id1, "export-") {
		t.Fatalf("recorded as %q", id1)
	}
	r1, err := evidence.VerifyExport(x1, pub, nil)
	if err != nil || r1.Verdict != evidence.OK || !r1.Sums.SignatureValid || r1.Sums.KeyID != evidence.KeyID(pub) || len(r1.Ledger.Heads) != 1 {
		t.Fatalf("export 1: %v %+v %v", err, r1.Sums, r1.AllFindings())
	}
	if len(r1.Exports) != 0 {
		t.Fatalf("export 1 records exports before it: %+v", r1.Exports)
	}
	readme, _ := os.ReadFile(filepath.Join(x1, "README.txt"))
	if !strings.Contains(string(readme), "1. FIRST compare both key_ids") || !strings.Contains(string(readme), "-export .") ||
		!strings.Contains(string(readme), id1) || !strings.Contains(string(readme), "SHA256SUMS.sig") {
		t.Fatalf("README:\n%s", readme)
	}

	// The record: in the catalog, in the server's ledger, sealed, its manifest describing the export.
	after, _ := f.s.db.EvidenceBundles(ctx, 0, 100)
	if len(after) != len(before)+1 || after[0].BundleID != id1 || after[0].TestID != nil || after[0].VMID != nil || after[0].MigrationCheckID != nil ||
		after[0].Verdict != evidence.OK {
		t.Fatalf("catalog: %+v", after)
	}
	sr, err := evidence.VerifyLedger(f.s.cfg.ledgerPath(), f.s.cfg.bundlesDir(), pub)
	if err != nil || sr.Verdict != evidence.OK || sr.Entries != after[0].LedgerSeq || sr.Entries != r1.Ledger.Entries+1 {
		t.Fatalf("the server's ledger: %v %d entries %v", err, sr.Entries, sr.AllFindings())
	}
	mb, err := os.ReadFile(filepath.Join(f.s.cfg.bundlesDir(), id1, evidence.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Subject map[string]string `json:"subject"`
		Export  ExportManifest    `json:"export"`
	}
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatal(err)
	}
	sumsHash := r1.Sums.SHA256
	ex := man.Export
	if man.Subject["kind"] != evidence.ExportBundleKind || man.Subject["user"] != "auditor1" || man.Subject["sha256sums_sha256"] != sumsHash ||
		ex.Format != exportFormat || ex.By.User != "auditor1" || ex.By.Role != store.RoleAuditor || ex.By.Via != "session" ||
		ex.Scope.Kind != "all" || ex.Scope.LedgerEntries != r1.Ledger.Entries || ex.SumsSHA256 != sumsHash || ex.SumsKeyID != evidence.KeyID(pub) ||
		ex.Files != r1.Sums.Listed || ex.Period.From == "" || ex.Period.To == "" || ex.Name != filepath.Base(x1) || ex.Scope.DailyReport == nil {
		t.Fatalf("manifest:\n%s", mb)
	}
	sb, _ := os.ReadFile(filepath.Join(f.s.cfg.bundlesDir(), id1, "export", evidence.SumsFile))
	if string(sb) != string(must(os.ReadFile(filepath.Join(x1, evidence.SumsFile)))) {
		t.Fatal("the record does not hold the export's SHA256SUMS")
	}
	audits, _ := f.s.db.Audit(ctx, 0, 100000)
	found := false
	for _, a := range audits {
		if a.Target == "evidence:export" && strings.Contains(a.Detail, id1) {
			found = true
		}
	}
	if !found {
		t.Fatal("the audit log does not name the export's record")
	}

	// The next export contains the record, and both verify; the record is listed with the first export's SUMS hash.
	b2, id2 := f.export(store.RoleAdmin)
	if id2 == id1 {
		t.Fatal("two exports, one record")
	}
	x2 := untarExport(t, b2)
	r2, err := evidence.VerifyExport(x2, pub, nil)
	if err != nil || r2.Verdict != evidence.OK || len(r2.Exports) != 1 || r2.Exports[0].BundleID != id1 || r2.Exports[0].SumsSHA256 != sumsHash ||
		r2.Exports[0].Verdict != evidence.OK || r2.Exports[0].User != "auditor1" {
		t.Fatalf("export 2: %v %+v %v", err, r2.Exports, r2.AllFindings())
	}

	// The ledger checks after the exports: incremental (hourly, "Check now") and full (daily).
	for _, kind := range []string{store.LedgerCheckIncremental, store.LedgerCheckFull} {
		if c, err := f.s.checkLedger(ctx, "system", "after", kind); err != nil || c.Verdict != evidence.OK || c.Kind != kind || c.HeadSeq != sr.Entries+1 {
			t.Fatalf("%s check after the exports: %+v %v", kind, c, err)
		}
	}
	if err := f.s.ledgerCheckIfDue(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if cs, _ := f.s.db.LedgerChecks(ctx, 1); cs[0].Verdict != evidence.OK || cs[0].Kind != store.LedgerCheckIncremental {
		t.Fatalf("hourly check: %+v", cs[0])
	}
	if l, err := f.s.ledgerSummary(ctx); err != nil || l.Code != ledgerOK {
		t.Fatalf("summary: %+v %v", l, err)
	}

	// The Evidence screen shows the records as exports, by whom.
	var v EvidenceView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/evidence?kind=export", "", &v); st != 200 || v.Total != 2 || v.Rows[0].Kind != "export" ||
		v.Rows[0].BundleID != id2 || v.Rows[0].VM != "" || v.Rows[1].By != "auditor1" {
		t.Fatalf("evidence view: %d %+v", st, v)
	}
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/evidence?kind=test", "", &v); st != 200 {
		t.Fatalf("tests: %d", st)
	}
	for _, r := range v.Rows {
		if r.Kind != "test" {
			t.Fatalf("kind=test: %+v", r)
		}
	}

	// An export that cannot be recorded is not handed out: a damaged ledger refuses the append.
	data, _ := os.ReadFile(f.s.cfg.ledgerPath())
	if err := os.WriteFile(f.s.cfg.ledgerPath(), bytes.Replace(data, []byte(`\"verdict\":\"OK\"`), []byte(`\"verdict\":\"KO\"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	var e apiError
	tok := f.tokens[store.RoleAuditor]
	if st := f.doH(tok, "GET", "/api/v1/evidence-export", "", f.stepUp(tok, "GET", "/api/v1/evidence-export"), &e); st == 200 ||
		!strings.Contains(e.Error, "not handed out") {
		t.Fatalf("an export of a damaged ledger: %d %+v", st, e)
	}
	ents, _ := os.ReadDir(f.s.cfg.bundlesDir())
	for _, d := range ents {
		if d.Name() != id1 && d.Name() != id2 && strings.HasPrefix(d.Name(), "export-") {
			t.Fatalf("an export that was refused left its record %s", d.Name())
		}
	}
}
