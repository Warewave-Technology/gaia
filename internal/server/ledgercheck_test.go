package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// ADR-007 C14: hourly incremental checks with a daily full one, the kind recorded on the check, and the ledger
// summary's status and code by the rule (ok, waiting, check_overdue, check_failed, never_checked, ledger_broken).
func TestLedgerChecksAndSummary(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	clock := time.Now().UTC().Truncate(time.Second)
	f.s.now = func() time.Time { return clock }
	f.s.db.SetClock(func() time.Time { return clock })
	summary := func(wantStatus, wantCode string) LedgerSummary {
		t.Helper()
		l, err := f.s.ledgerSummary(ctx)
		if err != nil || l.Status != wantStatus || l.Code != wantCode {
			t.Fatalf("summary: want %s %s, got %+v %v", wantStatus, wantCode, l, err)
		}
		return l
	}
	summary(status.Unknown, ledgerNever)

	f.seed() // one sealed bundle (entry 2), no check yet
	key, err := evidence.LoadPrivateKey(f.s.cfg.keyPath())
	if err != nil {
		t.Fatal(err)
	}
	seal := func(id string) {
		t.Helper()
		dir := filepath.Join(f.s.cfg.bundlesDir(), id)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"verdict":"OK"}`), 0o644)
		sha, err := evidence.Seal(dir, id, map[string]string{"vm": "seed-vm"}, key)
		if err != nil {
			t.Fatal(err)
		}
		le, err := evidence.Append(f.s.cfg.ledgerPath(), evidence.Entry{BundleID: id, BundleSHA256: sha, Subject: "seed-vm", Verdict: "OK"}, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.db.AddEvidence(ctx, store.EvidenceBundle{BundleID: id, LedgerSeq: le.Seq, BundleSHA256: sha, Verdict: "OK"}); err != nil {
			t.Fatal(err)
		}
	}
	// The first hourly check: full (no full check in a day).
	if err := f.s.ledgerCheckIfDue(ctx, clock); err != nil {
		t.Fatal(err)
	}
	cs, _ := f.s.db.LedgerChecks(ctx, 10)
	if len(cs) != 1 || cs[0].Kind != store.LedgerCheckFull || cs[0].Verdict != evidence.OK || cs[0].Reason != "hourly" {
		t.Fatalf("first hourly check: %+v", cs)
	}
	summary(status.OK, ledgerOK)
	// Not due again within the hour.
	clock = clock.Add(30 * time.Minute)
	if err := f.s.ledgerCheckIfDue(ctx, clock); err != nil {
		t.Fatal(err)
	}
	if cs, _ := f.s.db.LedgerChecks(ctx, 10); len(cs) != 1 {
		t.Fatalf("checked again within the hour: %+v", cs)
	}
	// A new entry waits: OK, with how many and since when.
	seal("t-new-1")
	l := summary(status.OK, ledgerWaiting)
	if l.Unchecked != 1 || l.OldestUncheckedAt != clock.Format(time.RFC3339Nano) {
		t.Fatalf("waiting: %+v", l)
	}
	// The next hour: an incremental check continues the last one.
	clock = clock.Add(31 * time.Minute)
	if err := f.s.ledgerCheckIfDue(ctx, clock); err != nil {
		t.Fatal(err)
	}
	cs, _ = f.s.db.LedgerChecks(ctx, 10)
	if len(cs) != 2 || cs[0].Kind != store.LedgerCheckIncremental || cs[0].Verdict != evidence.OK || cs[0].HeadSeq != 3 {
		t.Fatalf("incremental: %+v", cs[0])
	}
	summary(status.OK, ledgerOK)
	// "Check now" is incremental.
	var now store.LedgerCheck
	if code := f.do(f.login(store.RoleAdmin), "POST", "/api/v1/ledger/verify", "", &now); code != 200 || now.Kind != store.LedgerCheckIncremental {
		t.Fatalf("check now: %d %+v", code, now)
	}
	// No successful check for 2 hours: overdue. Entries waiting for 2 hours: overdue too.
	clock = clock.Add(2 * time.Hour)
	summary(status.Unknown, ledgerOverdue)
	clock = clock.Add(-2*time.Hour + time.Minute)
	seal("t-new-2")
	summary(status.OK, ledgerWaiting)
	clock = clock.Add(2 * time.Hour)
	summary(status.Unknown, ledgerOverdue)
	// A day after the full check, the hourly one is full again.
	clock = clock.Add(24 * time.Hour)
	if err := f.s.ledgerCheckIfDue(ctx, clock); err != nil {
		t.Fatal(err)
	}
	if cs, _ := f.s.db.LedgerChecks(ctx, 1); cs[0].Kind != store.LedgerCheckFull || cs[0].Verdict != evidence.OK {
		t.Fatalf("daily full: %+v", cs[0])
	}
	// A new bundle changed after sealing: the incremental check finds it.
	seal("t-new-3")
	os.WriteFile(filepath.Join(f.s.cfg.bundlesDir(), "t-new-3", "result.json"), []byte(`{"verdict":"CRITICAL"}`), 0o644)
	if c, err := f.s.checkLedger(ctx, "system", "test", store.LedgerCheckIncremental); err != nil || c.Verdict != evidence.Critical || c.Kind != store.LedgerCheckIncremental {
		t.Fatalf("damaged new bundle: %+v %v", c, err)
	}
	summary(status.Critical, ledgerBroken)
	// A check that cannot run is recorded as such: check_failed, not the older verdict.
	os.WriteFile(filepath.Join(f.s.cfg.bundlesDir(), "t-new-3", "result.json"), []byte(`{"verdict":"OK"}`), 0o644)
	if c, err := f.s.checkLedger(ctx, "system", "test", store.LedgerCheckFull); err != nil || c.Verdict != evidence.OK {
		t.Fatalf("repaired: %+v %v", c, err)
	}
	pub := f.s.cfg.pubPath()
	b, _ := os.ReadFile(pub)
	os.Remove(pub)
	if _, err := f.s.checkLedger(ctx, "system", "test", store.LedgerCheckIncremental); err == nil {
		t.Fatal("a check without the evidence key ran")
	}
	l = summary(status.Unknown, ledgerCheckFailed)
	if l.Verdict != evidence.Unknown || len(l.Problems) != 1 {
		t.Fatalf("failed check: %+v", l)
	}
	os.WriteFile(pub, b, 0o644)
}

// A restore test passed but the ledger has no entries: CRITICAL evidence_missing, checked or not (ADR-007 C14).
func TestLedgerEvidenceMissing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	if _, err := f.s.checkLedger(ctx, "system", "test", store.LedgerCheckFull); err != nil {
		t.Fatal(err)
	}
	if l, _ := f.s.ledgerSummary(ctx); l.Code != ledgerOK {
		t.Fatalf("empty, no tests: %+v", l)
	}
	h, err := f.s.db.CreateHost(ctx, store.HostInput{Name: "h", Address: "h.lab", SSHUser: "gaia"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.s.db.CreateVM(ctx, "admin", store.VMInput{HostID: h.ID, Name: "v"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.s.db.CreateRepository(ctx, store.Repository{Name: "r", Kind: "filesystem", Location: t.TempDir(), Splitter: "FIXED-4M", Hash: "BLAKE2B-256-128"})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := f.s.db.CreateJob(ctx, "system", "backup", &v.ID, "t")
	p, err := f.s.db.CreatePoint(ctx, "system", store.Point{VMID: v.ID, JobID: j.ID, RepositoryID: r.ID, Kind: "full", TakenAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	tj, _ := f.s.db.CreateJob(ctx, "system", "restore_test", &v.ID, "t")
	rt, err := f.s.db.CreateRestoreTest(ctx, "system", store.RestoreTest{VMID: v.ID, PointID: p.ID, JobID: tj.ID})
	if err != nil {
		t.Fatal(err)
	}
	rto := 30.0
	if _, err := f.s.db.FinishRestoreTest(ctx, "system", rt.ID, "OK", &rto, "passed", "", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if l, err := f.s.ledgerSummary(ctx); err != nil || l.Status != status.Critical || l.Code != ledgerNoEvidence {
		t.Fatalf("a passed test without evidence: %+v %v", l, err)
	}
}
