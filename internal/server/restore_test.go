package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/recoverykit"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
)

// The recovery kit through the API: written at init and again with each repository, handed out to admins only,
// confirmed only for the newest generation and the exact file; a new passphrase writes a new kit.
func TestRecoveryKitAPI(t *testing.T) {
	f := newFixture(t)
	admin := f.tokens[store.RoleAdmin]
	var info KitInfo
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/recovery-kit", "", &info); st != 200 || info.Summary.State != "not confirmed" || info.Summary.Generation != 1 {
		t.Fatalf("after init: %d %+v", st, info)
	}
	if st := f.do(admin, "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q}`, filepath.Join(f.dir, "repo")), nil); st != 400 {
		t.Fatalf("a repository without the kit passphrase: %d", st)
	}
	if st := f.do(admin, "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q,"kit_passphrase":"not the passphrase"}`,
		filepath.Join(f.dir, "repo")), nil); st != 400 {
		t.Fatalf("a repository with a wrong kit passphrase: %d", st)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "repo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a repository was made although the passphrase was wrong: %v", err)
	}
	if st := f.do(admin, "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q,"kit_passphrase":%q}`,
		filepath.Join(f.dir, "repo"), kitPassphrase), nil); st != 201 {
		t.Fatalf("repository: %d", st)
	}
	f.do(admin, "GET", "/api/v1/recovery-kit", "", &info)
	if info.Summary.Generation != 2 || len(info.Kits) != 2 || info.Kits[0].Repositories[0] != "main" || info.Kits[0].Reason != "repository main created" {
		t.Fatalf("after the repository: %+v", info)
	}
	var kit []byte
	if st := f.do(f.tokens[store.RoleOperator], "GET", "/api/v1/recovery-kit/file", "", &kit); st != 403 {
		t.Fatalf("an operator fetched the kit: %d", st)
	}
	if st := f.do(admin, "GET", "/api/v1/recovery-kit/file", "", &kit); st != 200 {
		t.Fatalf("kit file: %d", st)
	}
	k, err := recoverykit.Open(kit, kitPassphrase)
	if err != nil || k.Generation != 2 || k.StateRepository != "main" || len(k.Repositories) != 1 || k.SSHKey == "" || k.TLSKey == "" {
		t.Fatalf("the kit: %+v %v", k, err)
	}
	if pw, _ := os.ReadFile(f.s.repoPasswordFile("main")); strings.TrimSpace(string(pw)) != k.Repositories[0].Password {
		t.Fatal("the kit's repository password is not the server's")
	}
	if st := f.do(admin, "POST", "/api/v1/recovery-kit/confirm", `{"generation":1,"sha256":"`+recoverykit.SHA256(kit)+`"}`, nil); st != 409 {
		t.Fatalf("confirming an old generation: %d", st)
	}
	if st := f.do(admin, "POST", "/api/v1/recovery-kit/confirm", `{"generation":2,"sha256":"`+recoverykit.SHA256([]byte("other"))+`"}`, nil); st != 409 {
		t.Fatalf("confirming another file: %d", st)
	}
	if st := f.do(admin, "POST", "/api/v1/recovery-kit/confirm", `{"generation":2,"sha256":"`+recoverykit.SHA256(kit)+`"}`, nil); st != 200 {
		t.Fatalf("confirm: %d", st)
	}
	var sys SystemInfo
	f.do(admin, "GET", "/api/v1/system", "", &sys)
	if sys.RecoveryKit.State != "confirmed" || sys.RecoveryKit.Generation != 2 {
		t.Fatalf("system after confirming: %+v", sys.RecoveryKit)
	}
	if st := f.do(admin, "POST", "/api/v1/recovery-kit", `{"passphrase":"not the passphrase"}`, nil); st != 400 {
		t.Fatalf("regenerate with a wrong passphrase: %d", st)
	}
	if st := f.do(admin, "POST", "/api/v1/recovery-kit", fmt.Sprintf(`{"passphrase":%q,"new_passphrase":"a new kit passphrase"}`, kitPassphrase), nil); st != 201 {
		t.Fatalf("new passphrase: %d", st)
	}
	f.do(admin, "GET", "/api/v1/recovery-kit/file", "", &kit)
	if _, err := recoverykit.Open(kit, kitPassphrase); !errors.Is(err, recoverykit.ErrPassphrase) {
		t.Fatalf("the old passphrase still opens the new kit: %v", err)
	}
	if k, err := recoverykit.Open(kit, "a new kit passphrase"); err != nil || k.Generation != 3 {
		t.Fatalf("kit 3: %+v %v", k, err)
	}
	f.do(admin, "GET", "/api/v1/system", "", &sys)
	if sys.RecoveryKit.State != "not confirmed" || sys.RecoveryKit.Generation != 3 {
		t.Fatalf("a new kit must be saved again: %+v", sys.RecoveryKit)
	}
	var audit []store.AuditEntry
	f.do(admin, "GET", "/api/v1/audit?limit=50", "", &audit)
	fetched := 0
	for _, a := range audit {
		if a.Action == "GET /api/v1/recovery-kit/file" && a.Outcome == 200 {
			fetched++
		}
		if strings.Contains(a.Detail, kitPassphrase) {
			t.Fatalf("the passphrase is in the audit log: %+v", a)
		}
	}
	if fetched != 2 {
		t.Fatalf("kit downloads in the audit log: %d", fetched)
	}
}

// testedVM gives the fixture a repository, a VM with a checked two-disk point, and a sandbox that passes; test()
// runs the VM's due restore test at the given time.
func testedVM(t *testing.T, f *fixture) (store.VM, store.Repository, func(time.Time) store.RestoreTest) {
	ctx := context.Background()
	s := f.s
	sockDir, err := os.MkdirTemp("", "gts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	s.cfg.SandboxImagesDir = filepath.Join(f.dir, "sandbox-images")
	os.MkdirAll(s.cfg.SandboxImagesDir, 0o755)
	sock := filepath.Join(sockDir, "s.sock")
	s.sandbox = sandbox.NewClient(sock)
	var repo store.Repository
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q,"kit_passphrase":%q}`,
		filepath.Join(f.dir, "repo"), kitPassphrase), &repo); st != 201 {
		t.Fatalf("repository: %d", st)
	}
	h, _ := s.db.CreateHost(ctx, store.HostInput{Name: "kvm", Address: "kvm", SSHUser: "gaia-backup"})
	pol, err := s.db.CreatePolicy(ctx, store.PolicyInput{Name: "p", RepositoryID: repo.ID, BackupIntervalS: 900, TestIntervalS: 3600, KeepLast: 4,
		RPOTargetS: 8 * 3600, RTOTargetS: 600, MaxProofAgeS: 8 * 3600, CheckTemplate: "linux-boot"})
	if err != nil {
		t.Fatal(err)
	}
	vm, _ := s.db.CreateVM(ctx, "admin", store.VMInput{HostID: h.ID, Name: "web1", PolicyID: &pol.ID})
	data := map[string][]byte{"vda": bytes.Repeat([]byte("boot disk "), 300_000), "vdb": bytes.Repeat([]byte("data "), 100_000)}
	st, err := s.OpenRepository(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := s.db.CreateJob(ctx, "system", "backup", &vm.ID, "test")
	var pds []store.PointDisk
	for _, target := range []string{"vda", "vdb"} {
		b := data[target]
		sp, err := st.Backup(ctx, diskstore.BackupRequest{Domain: "web1", Disk: target, Checkpoint: "c1", Size: uint64(len(b)),
			Changed: []diskstore.Range{{Offset: 0, Length: uint64(len(b))}}, Sources: []diskstore.Source{bytes.NewReader(b)}})
		if err != nil {
			t.Fatal(err)
		}
		pds = append(pds, store.PointDisk{Target: target, SizeBytes: int64(len(b)), BlockSize: diskstore.BlockSize, StoreID: sp.ID, WrittenBytes: int64(len(b))})
	}
	st.Close(ctx)
	p, err := s.db.CreateBackupPoint(ctx, "system", store.Point{VMID: vm.ID, JobID: j.ID, RepositoryID: repo.ID, Kind: "full", Checkpoint: "gaia-1-1",
		TakenAt: time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano),
		Machine: json.RawMessage(`{"arch":"x86_64","firmware":"bios","memory_mib":1024,"vcpus":2,"disk_bus":"virtio","nic_model":"","mac":""}`)}, pds)
	if err != nil {
		t.Fatal(err)
	}
	s.db.SetPointCheck(ctx, "system", p.ID, true, "test")
	s.db.SetJobState(ctx, "system", j.ID, "running", "test", "")
	s.db.SetJobState(ctx, "system", j.ID, "ok", "test", "")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: &fakeSandbox{t: t, want: data, verdict: verify.OK}}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return vm, repo, func(at time.Time) store.RestoreTest {
		s.now = func() time.Time { return at }
		if err := s.queueDueTests(ctx, at); err != nil {
			t.Fatal(err)
		}
		qs, _ := s.db.QueuedJobs(ctx)
		if len(qs) != 1 || qs[0].Kind != "restore_test" {
			t.Fatalf("queued %+v", qs)
		}
		s.runTest(ctx, qs[0])
		ts, _ := s.db.RestoreTests(ctx, vm.ID, 1)
		return ts[0]
	}
}

// A disaster-recovery drill in one process. A server with a VM, a point and a passed restore test backs its state
// up; then it tests again and stores another point, and is lost. A new server is rebuilt from the recovery kit and
// the repository alone: the catalog, ledger, evidence and status are those of the state backup, the ledger
// verifies, what came after the state backup is named, a new kit is written, and the server starts.
func TestRestoreState(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	vm, repo, test := testedVM(t, f)
	clock := time.Now().UTC()
	if rt := test(clock); rt.Verdict != "OK" {
		t.Fatalf("first test %+v", rt)
	}
	if err := f.s.refreshAllStatuses(ctx); err != nil {
		t.Fatal(err)
	}
	if b, err := f.s.BackupState(ctx, "admin", "drill"); err != nil || b.State != "ok" {
		t.Fatalf("state backup %+v %v", b, err)
	}
	st, err := f.s.OpenRepository(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if as, err := st.Archives(ctx, EvidenceArchiveKind); err != nil || len(as) != 1 {
		t.Fatalf("evidence archives %+v %v", as, err)
	}
	// After the state backup: another test, and a point the catalog will not know.
	if rt := test(clock.Add(2 * time.Hour)); rt.Verdict != "OK" {
		t.Fatalf("second test %+v", rt)
	}
	late, err := st.Backup(ctx, diskstore.BackupRequest{Domain: "web1", Disk: "vda", Checkpoint: "c9", Size: 1 << 20,
		Changed: []diskstore.Range{{Offset: 0, Length: 1 << 20}}, Sources: []diskstore.Source{bytes.NewReader(bytes.Repeat([]byte{7}, 1<<20))}})
	if err != nil {
		t.Fatal(err)
	}
	st.Close(ctx)
	kit, err := os.ReadFile(f.s.cfg.kitPath())
	if err != nil {
		t.Fatal(err)
	}
	var before VMStatus
	f.do(f.tokens[store.RoleAdmin], "GET", fmt.Sprintf("/api/v1/vms/%d/status", vm.ID), "", &before)

	// The new machine.
	nd := t.TempDir()
	o := RestoreOptions{ConfigPath: filepath.Join(nd, "etc", "server.json"), DataDir: filepath.Join(nd, "data"), Kit: kit, Passphrase: kitPassphrase}
	if _, err := RestoreState(ctx, RestoreOptions{ConfigPath: o.ConfigPath, DataDir: o.DataDir, Kit: kit, Passphrase: "not the passphrase"}); !errors.Is(err, recoverykit.ErrPassphrase) {
		t.Fatalf("a wrong passphrase: %v", err)
	}
	other := newFixture(t)
	otherKit, _ := os.ReadFile(other.s.cfg.kitPath())
	if _, err := RestoreState(ctx, RestoreOptions{ConfigPath: o.ConfigPath, DataDir: o.DataDir, Kit: otherKit, Passphrase: kitPassphrase}); err == nil ||
		!strings.Contains(err.Error(), "no repository") {
		t.Fatalf("a kit written before any repository: %v", err)
	}
	if des, _ := os.ReadDir(o.DataDir); len(des) != 0 {
		t.Fatalf("a failed rebuild left %v", des)
	}
	r, err := RestoreState(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreState(ctx, o); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("a second rebuild over the first: %v", err)
	}
	c := r.Catalog
	if c.VMs != 1 || c.Points != 1 || c.RestoreTests != 1 || c.EvidenceBundles != 1 || r.Evidence.Restored != 1 || r.Ledger.Verdict != "OK" || r.Ledger.Entries != 2 {
		t.Fatalf("restored %+v", r)
	}
	if r.Verdict != "WARNING" || len(r.Repositories) != 1 || len(r.Repositories[0].PointsNotInCatalog) != 1 || r.Repositories[0].PointsNotInCatalog[0] != late.ID {
		t.Fatalf("the point stored after the state backup is not named: %+v", r)
	}
	if r.KitGenerationIn != 2 || r.KitGeneration != 3 || r.SessionsEnded != 3 || len(r.Tag) != 6 || r.StateBackup.SchemaVersion != store.Latest() || r.EvidenceKeyID == "" {
		t.Fatalf("rebuild record %+v", r)
	}
	if fp, _ := CertFileFingerprint(f.s.cfg.TLSCert); fp != r.CertSHA256 {
		t.Fatal("the rebuilt server does not keep the lost server's certificate")
	}

	cfg, err := LoadConfig(o.ConfigPath)
	if err != nil || cfg.DataDir != o.DataDir {
		t.Fatalf("config %+v %v", cfg, err)
	}
	s2, err := New(ctx, cfg, o.ConfigPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.restoreTag != r.Tag || s2.sshKey.AuthorizedKey != f.s.sshKey.AuthorizedKey {
		t.Fatalf("restore tag %q, SSH key %q", s2.restoreTag, s2.sshKey.AuthorizedKey)
	}
	ts := httptest.NewServer(s2.Handler())
	defer ts.Close()
	f2 := &fixture{t: t, s: s2, ts: ts, dir: nd, tokens: map[string]string{}}
	f2.tokens[store.RoleAdmin] = f2.login(store.RoleAdmin) // the lost server's users came back
	admin := f2.tokens[store.RoleAdmin]
	var after VMStatus
	f2.do(admin, "GET", fmt.Sprintf("/api/v1/vms/%d/status", vm.ID), "", &after)
	if after.Status != before.Status || after.Status != "OK" || after.ChangedAt != before.ChangedAt {
		t.Fatalf("status before %+v after %+v", before, after)
	}
	var info KitInfo
	f2.do(admin, "GET", "/api/v1/recovery-kit", "", &info)
	if info.Summary.State != "not confirmed" || info.Summary.Generation != 3 || !strings.Contains(info.Kits[0].Reason, "rebuilt") {
		t.Fatalf("kit after the rebuild %+v", info)
	}
	evs, _ := s2.db.EvidenceBundles(ctx, 0, 10)
	var export []byte
	if st := f2.do(admin, "GET", fmt.Sprintf("/api/v1/evidence/%d/export", evs[0].ID), "", &export); st != 200 || len(export) == 0 {
		t.Fatalf("export of a restored bundle: %d", st)
	}
	var audit []store.AuditEntry
	f2.do(admin, "GET", "/api/v1/audit?limit=1000", "", &audit)
	found := false
	for _, a := range audit {
		found = found || (a.Action == "gaia server restore-state" && strings.Contains(a.Detail, r.StateBackup.ID))
	}
	var av struct {
		Intact bool `json:"intact"`
	}
	f2.do(admin, "GET", "/api/v1/audit/verify", "", &av)
	if !found || !av.Intact {
		t.Fatalf("audit after the rebuild: restore entry %v, intact %v", found, av.Intact)
	}
	st2, err := s2.OpenRepository(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close(ctx)
	pds, _ := s2.db.PointDisks(ctx, 1)
	if c, err := st2.CheckPoint(ctx, pds[0].StoreID); err != nil || len(c.Problems) != 0 {
		t.Fatalf("a restored point in the repository: %+v %v", c, err)
	}
	// The rebuilt server backs its state up into the same repository; the newest archive is its own.
	if b, err := s2.BackupState(ctx, "admin", "after the rebuild"); err != nil || b.State != "ok" {
		t.Fatalf("state backup after the rebuild %+v %v", b, err)
	}
}

func TestStateArchivesToPrune(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	var as []diskstore.Archive
	add := func(id string, ago time.Duration) {
		as = append(as, diskstore.Archive{ID: id, Created: now.Add(-ago)})
	}
	add("40d", 40*24*time.Hour)                // older than 30 days: goes
	add("40d-keep", 40*24*time.Hour+time.Hour) // ... unless it is the one just written
	add("5d-early", 5*24*time.Hour+3*time.Hour)
	add("5d-late", 5*24*time.Hour) // the newest of its day stays
	add("3d", 3*24*time.Hour)
	add("47h", 47*time.Hour) // the last two days: all stay
	add("1h", time.Hour)
	add("now", 0)
	slices.SortFunc(as, func(a, b diskstore.Archive) int { return a.Created.Compare(b.Created) })
	got := stateArchivesToPrune(as, "40d-keep", now)
	slices.Sort(got)
	if want := []string{"40d", "5d-early"}; !slices.Equal(got, want) {
		t.Fatalf("pruned %v, want %v", got, want)
	}
}

// A job's end leads to one state backup, not one per job.
func TestStateBackupAfterJobs(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q,"kit_passphrase":%q}`,
		filepath.Join(f.dir, "repo"), kitPassphrase), nil); st != 201 {
		t.Fatalf("repository: %d", st)
	}
	old := stateBackupGap
	stateBackupGap = 300 * time.Millisecond
	defer func() { stateBackupGap = old }()
	done := make(chan struct{})
	go func() { f.s.backupStateAfterChanges(ctx); close(done) }()
	for range 5 {
		f.s.stateChanged()
	}
	deadline := time.Now().Add(10 * time.Second)
	var bs []store.StateBackup
	for time.Now().Before(deadline) {
		bs, _ = f.s.db.StateBackups(ctx, 10)
		if len(bs) > 0 && bs[0].State != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(stateBackupGap + 200*time.Millisecond)
	cancel()
	<-done
	bs, _ = f.s.db.StateBackups(context.Background(), 10)
	if len(bs) != 1 || bs[0].State != "ok" || bs[0].Reason != "after jobs" {
		t.Fatalf("state backups after five job ends: %+v", bs)
	}
}
