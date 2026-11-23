package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// ADR-007 C19.3: the records of exports given while the ledger could not record them are in the state backup (one
// member each under evidence/pending-exports/), and restore-state re-queues them: the rebuilt server lists them as not
// recorded, says so in the rebuild's output and audit entry, and appends them with its next successful ledger write.
func TestRestoreStatePendingExports(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	good := damageLedger(t, f)
	_, h := f.exportPath(store.RoleAuditor, "/api/v1/evidence-export")
	p := h.Get("X-Gaia-Export-Pending-Record")
	if p == "" {
		t.Fatalf("no pending record: %v", h)
	}
	want, err := os.ReadFile(filepath.Join(f.s.cfg.pendingExportsDir(), p+".json"))
	if err != nil {
		t.Fatal(err)
	}
	// The ledger is repaired, but nothing has written to it since: the record is still pending at the state backup.
	if err := os.WriteFile(f.s.cfg.ledgerPath(), good, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := f.s.BackupState(ctx, "admin", "drill")
	if err != nil || b.State != "ok" {
		t.Fatalf("state backup %+v %v", b, err)
	}
	repo, _ := f.s.db.RepositoryByName(ctx, "main")
	st, err := f.s.OpenRepository(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	var arch bytes.Buffer
	if _, err := st.ReadArchive(ctx, b.SnapshotID, &arch); err != nil {
		t.Fatal(err)
	}
	st.Close(ctx)
	out := filepath.Join(f.dir, "archive")
	names := untar(t, &arch, out)
	if !slices.Equal(names, []string{"gaia.db", "server.json", "keys/evidence-key.pub", "evidence/ledger.jsonl", "evidence/pending-exports/" + p + ".json", "MANIFEST.json"}) {
		t.Fatalf("archive members %v", names)
	}
	var man StateManifest
	if err := json.Unmarshal(must(os.ReadFile(filepath.Join(out, "MANIFEST.json"))), &man); err != nil || man.PendingExports != 1 {
		t.Fatalf("manifest %+v %v", man, err)
	}
	if got := must(os.ReadFile(filepath.Join(out, "evidence", "pending-exports", p+".json"))); !bytes.Equal(got, want) {
		t.Fatalf("the archived record differs from the server's:\n%s\n%s", got, want)
	}

	// The rebuild re-queues it.
	kit := must(os.ReadFile(f.s.cfg.kitPath()))
	nd := t.TempDir()
	o := RestoreOptions{ConfigPath: filepath.Join(nd, "etc", "server.json"), DataDir: filepath.Join(nd, "data"), Kit: kit, Passphrase: kitPassphrase}
	r, err := RestoreState(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.PendingExports, []string{p}) || r.Ledger.Verdict != "OK" {
		t.Fatalf("rebuild: pending %v, ledger %+v, problems %v", r.PendingExports, r.Ledger, r.Problems)
	}
	if got := must(os.ReadFile(filepath.Join(o.DataDir, "evidence", "pending-exports", p+".json"))); !bytes.Equal(got, want) {
		t.Fatal("the re-queued record differs from the lost server's")
	}
	cfg, err := LoadConfig(o.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := New(ctx, cfg, o.ConfigPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	audits, _ := s2.db.Audit(ctx, 0, 1000)
	if !slices.ContainsFunc(audits, func(a store.AuditEntry) bool {
		return a.Action == "gaia server restore-state" && strings.Contains(a.Detail, "1 export record(s) the ledger had not recorded re-queued") && strings.Contains(a.Detail, p)
	}) {
		t.Fatal("the rebuild's audit entry does not name the re-queued record")
	}
	ts := httptest.NewServer(s2.Handler())
	defer ts.Close()
	f2 := &fixture{t: t, s: s2, ts: ts, dir: nd, tokens: map[string]string{}, keys: f.keys, owners: map[string]string{}}
	auditor := f2.login(store.RoleAuditor)
	var v EvidenceView
	if st := f2.do(auditor, "GET", "/api/v1/views/evidence", "", &v); st != http.StatusOK || v.NotRecorded != 1 || len(v.Rows) == 0 ||
		v.Rows[0].BundleID != p || v.Rows[0].LedgerRecord != ledgerNotRecorded {
		t.Fatalf("the rebuilt server's evidence view: %d %+v", st, v)
	}

	// The rebuilt server's next successful ledger write (the hourly check) appends it, as on the lost server.
	if err := s2.ledgerCheckIfDue(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ps, err := s2.pendingExports(); err != nil || len(ps) != 0 {
		t.Fatalf("still pending: %v %+v", err, ps)
	}
	if seq, _ := evidence.BundleEntry(s2.cfg.ledgerPath(), p); seq == 0 {
		t.Fatal("not appended")
	}
	scs, _ := s2.db.StateChanges(ctx, exportEntity, 0, 0, 10)
	if !slices.ContainsFunc(scs, func(c store.StateChange) bool {
		return c.From == exportNotRecorded && c.To == exportRecorded && strings.Contains(c.Reason, p)
	}) {
		t.Fatalf("state changes: %+v", scs)
	}
	f2.do(auditor, "GET", "/api/v1/views/evidence", "", &v)
	if v.NotRecorded != 0 || v.Rows[0].BundleID != p || v.Rows[0].LedgerRecord != ledgerRecordedLate || v.Rows[0].LedgerSeq == 0 {
		t.Fatalf("after the append: %+v", v)
	}
	pub, err := evidence.LoadPublicKey(s2.cfg.pubPath())
	if err != nil {
		t.Fatal(err)
	}
	if sr, err := evidence.VerifyLedger(s2.cfg.ledgerPath(), s2.cfg.bundlesDir(), pub); err != nil || sr.Verdict != evidence.OK {
		t.Fatalf("the rebuilt server's ledger: %v %v", err, sr.AllFindings())
	}
}

// A state archive's pending members are taken only as records of the bundle their name says; an archive from before
// 0.2.0 has none, and its restore makes no pending directory.
func TestRequeuePendingExports(t *testing.T) {
	rec := func(id string) []byte { return must(json.Marshal(pendingExport{BundleID: id})) }
	for name, files := range map[string]map[string][]byte{
		"a path out of the directory": {"evidence/pending-exports/../x.json": rec("x")},
		"not an export's record":      {"evidence/pending-exports/seed.json": rec("seed")},
		"another bundle's record":     {"evidence/pending-exports/export-a.json": rec("export-b")},
		"not JSON":                    {"evidence/pending-exports/export-a.json": []byte("{")},
	} {
		var man StateManifest
		for n := range files {
			man.Files = append(man.Files, FileEntry{Name: n})
		}
		cfg := Config{DataDir: t.TempDir()}
		if ids, err := requeuePendingExports(cfg, files, man); err == nil {
			t.Fatalf("%s: re-queued %v", name, ids)
		}
	}
	cfg := Config{DataDir: t.TempDir()}
	man := StateManifest{Files: []FileEntry{{Name: "gaia.db"}, {Name: "server.json"}, {Name: "keys/evidence-key.pub"}, {Name: "evidence/ledger.jsonl"}}}
	if ids, err := requeuePendingExports(cfg, map[string][]byte{}, man); err != nil || len(ids) != 0 {
		t.Fatalf("an archive without records: %v %v", ids, err)
	}
	if _, err := os.Stat(cfg.pendingExportsDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a pending directory for an archive without records: %v", err)
	}
	// A member only the tar holds (not in the manifest, so never checked) is not taken.
	files := map[string][]byte{"evidence/pending-exports/export-a.json": rec("export-a")}
	if ids, err := requeuePendingExports(cfg, files, man); err != nil || len(ids) != 0 {
		t.Fatalf("a member the manifest does not list: %v %v", ids, err)
	}
}
