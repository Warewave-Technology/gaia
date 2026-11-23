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
	"slices"
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
	b, h := f.exportPath(role, "/api/v1/evidence-export")
	return b, h.Get("X-Gaia-Export-Bundle")
}

// exportPath makes an export (the full one, or a bundle's) as a role, with its step-up, and returns it and the headers.
func (f *fixture) exportPath(role, path string) ([]byte, http.Header) {
	f.t.Helper()
	tok := f.tokens[role]
	var b []byte
	h := f.stepUp(tok, "GET", path)
	req, _ := http.NewRequest("GET", f.ts.URL+path, nil)
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
		f.t.Fatalf("export %s as %s: %d %s", path, role, resp.StatusCode, b)
	}
	return b, resp.Header
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
	// ADR-007 C18.6: the single-bundle export of the restore test's bundle (an audit entry, no ledger record).
	sb, _ := f.exportPath(store.RoleAuditor, fmt.Sprintf("/api/v1/evidence/%d/export", f.ids["evidence"]))
	write("single-1.tar.gz", sb)
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
	if m := readExportManifest(t, x1); m.LedgerRecord != (ExportLedgerRecord{State: evidence.ExportRecordSealedAfter, BundleID: id1}) ||
		m.Scope.Kind != evidence.ExportScopeAll || r1.Record.State != evidence.ExportRecordSealedAfter || r1.Record.BundleID != id1 || len(r1.Record.Findings) != 0 {
		t.Fatalf("EXPORT.json: %+v; verify: %+v", m, r1.Record)
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
}

// readExportManifest reads an unpacked export's EXPORT.json.
func readExportManifest(t *testing.T, dir string) ExportManifest {
	t.Helper()
	var m ExportManifest
	if err := json.Unmarshal(must(os.ReadFile(filepath.Join(dir, evidence.ExportManifestFile))), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// damageLedger changes the first bundle entry's verdict, so its signature no longer verifies and the ledger refuses
// appends; it returns the ledger as it was.
func damageLedger(t *testing.T, f *fixture) []byte {
	t.Helper()
	data := must(os.ReadFile(f.s.cfg.ledgerPath()))
	bad := bytes.Replace(data, []byte(`\"verdict\":\"OK\"`), []byte(`\"verdict\":\"KO\"`), 1)
	if bytes.Equal(bad, data) {
		t.Fatal("nothing to damage")
	}
	if err := os.WriteFile(f.s.cfg.ledgerPath(), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

// ADR-007 C18.5: an export the ledger cannot record is given, not refused. Its README and EXPORT.json say "not recorded
// in the ledger: <reason>", SHA256SUMS is still signed, it is an audit entry, a security notification
// (export_not_recorded) is sent from the state change, and gaia evidence verify -export reports export_not_recorded
// (UNKNOWN, never OK). Its record is kept as pending and appended by the next successful ledger write: "Check now" on
// a repaired ledger (two pending records, oldest first), and another export's successful append. Later exports list
// the records with why they were late.
func TestExportNotRecorded(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	hook, _ := notifyChannels(t, f)
	if _, err := f.s.makeDailyReport(ctx, "admin", "test"); err != nil {
		t.Fatal(err)
	}
	pub, err := evidence.LoadPublicKey(f.s.cfg.pubPath())
	if err != nil {
		t.Fatal(err)
	}
	catalog := func() []store.EvidenceBundle {
		t.Helper()
		ev, err := f.s.db.EvidenceBundles(ctx, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	before := len(catalog())
	good := damageLedger(t, f)

	// Given while the ledger refuses: 200, the server names the pending record and why.
	b1, h1 := f.exportPath(store.RoleAuditor, "/api/v1/evidence-export")
	p1 := h1.Get("X-Gaia-Export-Pending-Record")
	why := h1.Get("X-Gaia-Export-Not-Recorded")
	if h1.Get("X-Gaia-Export-Bundle") != "" || !strings.HasPrefix(p1, "export-") || !strings.Contains(why, "not intact") {
		t.Fatalf("headers: %v", h1)
	}
	x1 := untarExport(t, b1)
	readme := string(must(os.ReadFile(filepath.Join(x1, "README.txt"))))
	if !strings.Contains(readme, "NOT RECORDED IN THE LEDGER: ") || !strings.Contains(readme, "  not recorded in the ledger: the evidence ledger refused the entry: ") ||
		!strings.Contains(readme, "evidence bundle "+p1+" with its next successful ledger write") || strings.Contains(readme, "This export is recorded") {
		t.Fatalf("README:\n%s", readme)
	}
	m1 := readExportManifest(t, x1)
	if m1.LedgerRecord.State != evidence.ExportRecordNotRecorded || m1.LedgerRecord.BundleID != p1 || !strings.Contains(m1.LedgerRecord.Reason, "not intact") ||
		m1.By.User != "auditor1" || m1.Scope.Kind != evidence.ExportScopeAll || m1.SumsSHA256 != "" || m1.Files != 0 {
		t.Fatalf("EXPORT.json: %+v", m1)
	}
	r1, err := evidence.VerifyExport(x1, pub, nil)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	for _, f := range r1.AllFindings() {
		codes[f.Code] = f.Severity
	}
	// The export holds the damaged ledger (CRITICAL, as it should); its own record is UNKNOWN, its files and signature OK.
	if r1.Verdict != evidence.Critical || codes[evidence.CodeExportNotRecorded] != evidence.Unknown || codes[evidence.CodeEntrySignatureInvalid] != evidence.Critical ||
		!r1.Sums.SignatureValid || r1.Sums.Listed != r1.Sums.Matching || r1.Record.State != evidence.ExportRecordNotRecorded || r1.Record.BundleID != p1 {
		t.Fatalf("verify: %s %+v %v", r1.Verdict, r1.Record, r1.AllFindings())
	}
	// No record directory, no catalog row; the pending record holds this export's SHA256SUMS.
	if _, err := os.Stat(filepath.Join(f.s.cfg.bundlesDir(), p1)); !os.IsNotExist(err) {
		t.Fatalf("a record directory for a refused append: %v", err)
	}
	for _, d := range must(os.ReadDir(f.s.cfg.bundlesDir())) {
		if strings.HasPrefix(d.Name(), "export-") {
			t.Fatalf("a refused attempt left %s", d.Name())
		}
	}
	if len(catalog()) != before {
		t.Fatal("a catalog row for an export that is not recorded")
	}
	ps, err := f.s.pendingExports()
	if err != nil || len(ps) != 1 || ps[0].BundleID != p1 || sha256Hex([]byte(ps[0].Sums)) != r1.Sums.SHA256 || ps[0].Manifest.SumsSHA256 != r1.Sums.SHA256 ||
		ps[0].Sums != string(must(os.ReadFile(filepath.Join(x1, evidence.SumsFile)))) || ps[0].SumsSig != string(must(os.ReadFile(filepath.Join(x1, evidence.SumsSignatureFile)))) {
		t.Fatalf("pending: %v %+v", err, ps)
	}
	// The audit entry, the state change and the notification.
	audits, _ := f.s.db.Audit(ctx, 0, 100000)
	if !slices.ContainsFunc(audits, func(a store.AuditEntry) bool {
		return a.Target == "evidence:export" && a.Outcome == 200 && strings.Contains(a.Detail, "NOT recorded in the ledger") && strings.Contains(a.Detail, p1)
	}) {
		t.Fatal("no audit entry for the export that is not recorded")
	}
	scs, _ := f.s.db.StateChanges(ctx, exportEntity, 0, 0, 10)
	if len(scs) != 1 || scs[0].From != "" || scs[0].To != exportNotRecorded || scs[0].Actor != "auditor1" || !strings.Contains(scs[0].Reason, p1) {
		t.Fatalf("state changes: %+v", scs)
	}
	if err := f.s.notifyOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	hook.mu.Lock()
	sent := slices.ContainsFunc(hook.events, func(e Event) bool {
		return e.Event == evExportNotRec && strings.Contains(e.Subject, exportNotRecordedSubject) && strings.Contains(e.Text, "not intact")
	})
	hook.mu.Unlock()
	if !sent {
		t.Fatal("no export_not_recorded notification")
	}

	// A second export while the ledger still refuses; a ledger check of the damaged ledger appends nothing.
	_, h2 := f.exportPath(store.RoleAdmin, "/api/v1/evidence-export")
	p2 := h2.Get("X-Gaia-Export-Pending-Record")
	var lc store.LedgerCheck
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/ledger/verify", "", &lc); st != 200 || lc.Verdict != evidence.Critical {
		t.Fatalf("check now: %d %+v", st, lc)
	}
	if ps, _ := f.s.pendingExports(); len(ps) != 2 || ps[0].BundleID != p1 || ps[1].BundleID != p2 {
		t.Fatalf("pending: %+v", ps)
	}

	// Repaired: "Check now" finds the ledger intact, and the next ledger write records both, oldest first.
	if err := os.WriteFile(f.s.cfg.ledgerPath(), good, 0o644); err != nil {
		t.Fatal(err)
	}
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/ledger/verify", "", &lc); st != 200 || lc.Verdict != evidence.OK {
		t.Fatalf("check now: %d %+v", st, lc)
	}
	if ps, err := f.s.pendingExports(); err != nil || len(ps) != 0 {
		t.Fatalf("still pending: %v %+v", err, ps)
	}
	s1, _ := evidence.BundleEntry(f.s.cfg.ledgerPath(), p1)
	s2, _ := evidence.BundleEntry(f.s.cfg.ledgerPath(), p2)
	if s1 == 0 || s2 != s1+1 {
		t.Fatalf("entries %d %d", s1, s2)
	}
	if ev := catalog(); len(ev) != before+2 || ev[1].BundleID != p1 || ev[1].LedgerSeq != s1 || ev[0].BundleID != p2 {
		t.Fatalf("catalog: %+v", ev[:2])
	}
	if sr, err := evidence.VerifyLedger(f.s.cfg.ledgerPath(), f.s.cfg.bundlesDir(), pub); err != nil || sr.Verdict != evidence.OK {
		t.Fatalf("the server's ledger: %v %v", err, sr.AllFindings())
	}
	var man struct {
		Subject map[string]string `json:"subject"`
		Export  ExportManifest    `json:"export"`
	}
	if err := json.Unmarshal(must(os.ReadFile(filepath.Join(f.s.cfg.bundlesDir(), p1, evidence.ManifestFile))), &man); err != nil {
		t.Fatal(err)
	}
	if man.Subject["kind"] != evidence.ExportBundleKind || man.Subject["recorded_late"] != "true" || man.Subject["not_recorded_reason"] != m1.LedgerRecord.Reason ||
		man.Subject["sha256sums_sha256"] != r1.Sums.SHA256 || man.Subject["given"] != m1.Made || man.Export.SumsSHA256 != r1.Sums.SHA256 ||
		man.Export.LedgerRecord != m1.LedgerRecord || man.Export.Name != filepath.Base(x1) {
		t.Fatalf("the late record: %+v", man)
	}
	scs, _ = f.s.db.StateChanges(ctx, exportEntity, 0, 0, 10)
	if len(scs) != 4 || scs[2].From != exportNotRecorded || scs[2].To != exportRecorded || !strings.Contains(scs[2].Reason, fmt.Sprintf("ledger entry %d", s1)) {
		t.Fatalf("state changes: %+v", scs)
	}

	// A later export is recorded as usual and lists both late records with why.
	b3, id3 := f.export(store.RoleAuditor)
	x3 := untarExport(t, b3)
	r3, err := evidence.VerifyExport(x3, pub, nil)
	if err != nil || r3.Verdict != evidence.OK || r3.Record.State != evidence.ExportRecordSealedAfter || r3.Record.BundleID != id3 || len(r3.Exports) != 2 ||
		r3.Exports[0].BundleID != p1 || r3.Exports[0].SumsSHA256 != r1.Sums.SHA256 || r3.Exports[0].NotRecordedReason != m1.LedgerRecord.Reason ||
		r3.Exports[1].BundleID != p2 {
		t.Fatalf("export 3: %v %s %+v %+v %v", err, r3.Verdict, r3.Record, r3.Exports, r3.AllFindings())
	}

	// Damaged again, then repaired: the next export's own successful append records the pending one after itself.
	good = damageLedger(t, f)
	_, h4 := f.exportPath(store.RoleAuditor, "/api/v1/evidence-export")
	p4 := h4.Get("X-Gaia-Export-Pending-Record")
	if p4 == "" {
		t.Fatalf("headers: %v", h4)
	}
	if err := os.WriteFile(f.s.cfg.ledgerPath(), good, 0o644); err != nil {
		t.Fatal(err)
	}
	_, id5 := f.export(store.RoleAdmin)
	s4, _ := evidence.BundleEntry(f.s.cfg.ledgerPath(), p4)
	s5, _ := evidence.BundleEntry(f.s.cfg.ledgerPath(), id5)
	if s5 == 0 || s4 != s5+1 {
		t.Fatalf("entries: export 5 %d, the pending record %d", s5, s4)
	}
	if ps, _ := f.s.pendingExports(); len(ps) != 0 {
		t.Fatalf("still pending: %+v", ps)
	}
	if c, err := f.s.checkLedger(ctx, "system", "after", store.LedgerCheckFull); err != nil || c.Verdict != evidence.OK {
		t.Fatalf("ledger check: %+v %v", c, err)
	}
}

// A ledger write that succeeds elsewhere (a restore test's bundle, the hourly check) appends the pending records; one
// that the ledger still refuses stays, with its attempt noted.
func TestPendingExportAfterLedgerWrite(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	good := damageLedger(t, f)
	_, h := f.exportPath(store.RoleAuditor, "/api/v1/evidence-export")
	p := h.Get("X-Gaia-Export-Pending-Record")
	f.s.afterLedgerWrite(ctx) // the ledger still refuses
	ps, err := f.s.pendingExports()
	if err != nil || len(ps) != 1 || ps[0].BundleID != p || ps[0].Attempts != 1 || !strings.Contains(ps[0].LastError, "not intact") {
		t.Fatalf("pending after a refused attempt: %v %+v", err, ps)
	}
	if _, err := os.Stat(filepath.Join(f.s.cfg.bundlesDir(), p)); !os.IsNotExist(err) {
		t.Fatalf("a refused attempt left its directory: %v", err)
	}
	if err := os.WriteFile(f.s.cfg.ledgerPath(), good, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ledgerCheckIfDue(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ps, _ := f.s.pendingExports(); len(ps) != 0 {
		t.Fatalf("still pending after the hourly check: %+v", ps)
	}
	if seq, _ := evidence.BundleEntry(f.s.cfg.ledgerPath(), p); seq == 0 {
		t.Fatal("not appended")
	}
	// An attempt that stopped after its append (the entry is there, the file still is): not appended twice.
	ev, _ := f.s.db.EvidenceBundles(ctx, 0, 1)
	pe := ps0(t, f, p)
	if err := f.s.keepPendingExport(pe); err != nil {
		t.Fatal(err)
	}
	f.s.afterLedgerWrite(ctx)
	if ps, _ := f.s.pendingExports(); len(ps) != 0 {
		t.Fatalf("still pending: %+v", ps)
	}
	if ev2, _ := f.s.db.EvidenceBundles(ctx, 0, 1); ev2[0].ID != ev[0].ID {
		t.Fatalf("appended twice: %+v %+v", ev, ev2)
	}
	data := must(os.ReadFile(f.s.cfg.ledgerPath()))
	if bytes.Count(data, []byte(p)) != 1 {
		t.Fatal("the ledger names the record twice")
	}
}

// ps0 rebuilds the pending record of an appended export from its bundle (as if its file had not been removed).
func ps0(t *testing.T, f *fixture, id string) pendingExport {
	t.Helper()
	var man struct {
		Export ExportManifest `json:"export"`
	}
	dir := filepath.Join(f.s.cfg.bundlesDir(), id)
	if err := json.Unmarshal(must(os.ReadFile(filepath.Join(dir, evidence.ManifestFile))), &man); err != nil {
		t.Fatal(err)
	}
	return pendingExport{BundleID: id, Manifest: man.Export, Sums: string(must(os.ReadFile(filepath.Join(dir, "export", evidence.SumsFile)))),
		SumsSig: string(must(os.ReadFile(filepath.Join(dir, "export", evidence.SumsSignatureFile)))), StateID: 1}
}

// ADR-007 C18.6: the single-bundle export has a signed SHA256SUMS like the full export's and an EXPORT.json (scope
// bundle, audit_only); it is an audit entry, not a ledger bundle. gaia evidence verify -export verifies it: the files,
// the signature, every ledger entry and its one bundle (the others left out by design). Tampered, it is CRITICAL;
// without its signature UNKNOWN.
func TestSingleBundleExport(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	f.export(store.RoleAdmin) // a second bundle in the ledger: the export's record
	pub, err := evidence.LoadPublicKey(f.s.cfg.pubPath())
	if err != nil {
		t.Fatal(err)
	}
	ledgerBefore := must(os.ReadFile(f.s.cfg.ledgerPath()))
	e, err := f.s.db.Evidence(ctx, f.ids["evidence"])
	if err != nil {
		t.Fatal(err)
	}
	b, h := f.exportPath(store.RoleAuditor, fmt.Sprintf("/api/v1/evidence/%d/export", e.ID))
	if h.Get("X-Gaia-Export-Bundle") != "" || h.Get("X-Gaia-Export-Pending-Record") != "" {
		t.Fatalf("a single-bundle export names a record: %v", h)
	}
	x := untarExport(t, b)
	if filepath.Base(x) != "gaia-evidence-"+e.BundleID {
		t.Fatalf("root %s", x)
	}
	r, err := evidence.VerifyExport(x, pub, nil)
	if err != nil || r.Verdict != evidence.OK || !r.Sums.SignatureValid || r.Sums.KeyID != evidence.KeyID(pub) || r.Sums.Listed != r.Sums.Matching ||
		r.Record.Scope != evidence.ExportScopeBundle || r.Record.Bundle != e.BundleID || r.Record.State != evidence.ExportRecordAuditOnly ||
		len(r.Ledger.Bundles) != 1 || r.Ledger.Bundles[0].BundleID != e.BundleID || r.Ledger.LeftOut != 1 || r.Ledger.Entries != 3 {
		t.Fatalf("verify: %v %s %+v %+v %v", err, r.Verdict, r.Sums, r.Record, r.AllFindings())
	}
	m := readExportManifest(t, x)
	if m.Scope.Kind != evidence.ExportScopeBundle || m.Scope.Bundle != e.BundleID || m.LedgerRecord.State != evidence.ExportRecordAuditOnly ||
		m.By.User != "auditor1" || m.LedgerRecord.BundleID != "" {
		t.Fatalf("EXPORT.json: %+v", m)
	}
	readme := string(must(os.ReadFile(filepath.Join(x, "README.txt"))))
	if !strings.Contains(readme, "gaia evidence verify -pubkey anchor.pub -export .") || !strings.Contains(readme, "not recorded in the evidence ledger") {
		t.Fatalf("README:\n%s", readme)
	}
	// Not a ledger bundle; an audit entry naming the SUMS hash.
	if !bytes.Equal(must(os.ReadFile(f.s.cfg.ledgerPath())), ledgerBefore) {
		t.Fatal("a single-bundle export wrote to the ledger")
	}
	audits, _ := f.s.db.Audit(ctx, 0, 100000)
	if !slices.ContainsFunc(audits, func(a store.AuditEntry) bool {
		return a.Action == "GET /api/v1/evidence/{id}/export" && a.Target == "evidence:"+e.BundleID && a.Username == "auditor1" &&
			strings.Contains(a.Detail, "SHA256SUMS "+r.Sums.SHA256) && strings.Contains(a.Detail, "no ledger record")
	}) {
		t.Fatal("no audit entry")
	}
	codesOf := func(dir string) (string, []string) {
		t.Helper()
		r, err := evidence.VerifyExport(dir, pub, nil)
		if err != nil {
			t.Fatal(err)
		}
		var cs []string
		for _, f := range r.AllFindings() {
			cs = append(cs, f.Code)
		}
		return r.Verdict, cs
	}
	// Tampered: a file of the bundle changed.
	bad := copyTree(t, x)
	if err := os.WriteFile(filepath.Join(bad, evidence.ExportBundlesDir, e.BundleID, "result.json"), []byte(`{"verdict":"KO"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, cs := codesOf(bad); v != evidence.Critical || !slices.Contains(cs, evidence.CodeSumsFileModified) || !slices.Contains(cs, evidence.CodeFileModified) {
		t.Fatalf("a bundle file changed: %s %v", v, cs)
	}
	// The bundle removed: missing, not left out.
	bad = copyTree(t, x)
	if err := os.RemoveAll(filepath.Join(bad, evidence.ExportBundlesDir, e.BundleID)); err != nil {
		t.Fatal(err)
	}
	if v, cs := codesOf(bad); v != evidence.Critical || !slices.Contains(cs, evidence.CodeBundleMissing) || !slices.Contains(cs, evidence.CodeSumsFileMissing) {
		t.Fatalf("the bundle removed: %s %v", v, cs)
	}
	// Without its signature (as an unsigned export before 0.2.0): UNKNOWN, never OK.
	bad = copyTree(t, x)
	if err := os.Remove(filepath.Join(bad, evidence.SumsSignatureFile)); err != nil {
		t.Fatal(err)
	}
	if v, cs := codesOf(bad); v != evidence.Unknown || !slices.Equal(cs, []string{evidence.CodeSumsSignatureMissing}) {
		t.Fatalf("no signature: %s %v", v, cs)
	}
}

func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}
