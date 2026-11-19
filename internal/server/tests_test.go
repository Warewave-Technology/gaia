package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
	"golang.org/x/crypto/ssh"
)

// fakeSandbox stands in for `gaia sandbox serve`: it checks that the disks it is given are the restored data, and
// answers with an unsigned bundle whose verdict the test chooses.
type fakeSandbox struct {
	t       *testing.T
	want    map[string][]byte // target -> the disk's data
	verdict string
	got     sandbox.Request
}

func (fs *fakeSandbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := json.NewDecoder(r.Body).Decode(&fs.got); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	for _, d := range fs.got.Disks {
		b, err := os.ReadFile(d.Path)
		if err != nil || !bytes.Equal(b, fs.want[d.Target]) {
			http.Error(w, fmt.Sprintf("disk %s is not the restored data (%v)", d.Target, err), 400)
			return
		}
	}
	res := verify.Result{Format: "gaia-verify-result/v1", BundleID: fmt.Sprintf("%s-%d", fs.got.Config.VM, time.Now().UnixNano()), VM: fs.got.Config.VM,
		Verdict: fs.verdict, Timings: map[string]float64{"restore": fs.got.RestoreS, "ready": 12}, Errors: []string{}}
	res.Checks = []verify.CheckResult{{Name: "boot", Type: verify.CheckBootSerial, Verdict: fs.verdict, Seconds: 12, Detail: "login prompt"}}
	if fs.verdict == verify.OK {
		res.Timings["proven_rto"] = fs.got.RestoreS + 12
	} else {
		res.Checks[0].Detail = "no login prompt within 300 s"
	}
	b, _ := json.Marshal(res)
	w.Header().Set("X-Gaia-Bundle-ID", res.BundleID)
	tw := tar.NewWriter(w)
	tw.WriteHeader(&tar.Header{Name: "result.json", Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg})
	tw.Write(b)
	tw.Close()
}

// A restore test end to end on the server: the newest checked point's two disks are restored into the sandbox
// directory, the helper gets exactly that data, its bundle is sealed into the ledger, the test and the VM status are
// recorded; a failing boot makes the VM CRITICAL; a helper that cannot be reached leaves the test UNKNOWN and the
// status CRITICAL.
func TestRestoreTestAndStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.s
	sockDir, err := os.MkdirTemp("", "gts") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	s.cfg.SandboxImagesDir = filepath.Join(f.dir, "sandbox-images")
	os.MkdirAll(s.cfg.SandboxImagesDir, 0o755)
	sock := filepath.Join(sockDir, "s.sock")
	s.sandbox = sandbox.NewClient(sock)
	clock := time.Now().UTC()
	s.now = func() time.Time { return clock }

	var repo store.Repository
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q,"kit_passphrase":%q}`, filepath.Join(f.dir, "repo"), kitPassphrase), &repo); st != 201 {
		t.Fatalf("repository: %d", st)
	}
	h, _ := s.db.CreateHost(ctx, store.HostInput{Name: "kvm", Address: "kvm", SSHUser: "gaia-backup"})
	pol, err := s.db.CreatePolicy(ctx, store.PolicyInput{Name: "p", RepositoryID: repo.ID, BackupIntervalS: 900, TestIntervalS: 3600, KeepLast: 4,
		RPOTargetS: 8 * 3600, RTOTargetS: 600, MaxProofAgeS: 8 * 3600, CheckTemplate: "linux-boot"})
	if err != nil {
		t.Fatal(err)
	}
	vm, _ := s.db.CreateVM(ctx, "admin", store.VMInput{HostID: h.ID, Name: "web1", PolicyID: &pol.ID})
	statusOf := func() VMStatus {
		var v VMStatus
		if st := f.do(f.tokens[store.RoleAuditor], "GET", fmt.Sprintf("/api/v1/vms/%d/status", vm.ID), "", &v); st != 200 {
			t.Fatalf("status: %d", st)
		}
		return v
	}
	if v := statusOf(); v.Status != "UNKNOWN" {
		t.Fatalf("a new VM: %+v", v)
	}

	// A point with two disks in the real repository.
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
	p, err := s.db.CreateBackupPoint(ctx, "system", store.Point{VMID: vm.ID, JobID: j.ID, RepositoryID: repo.ID, Kind: "full",
		TakenAt: clock.Add(-10 * time.Minute).Format(time.RFC3339Nano), Machine: json.RawMessage(`{"arch":"x86_64","firmware":"bios","memory_mib":1024,"vcpus":2,"disk_bus":"virtio","nic_model":"","mac":""}`)}, pds)
	if err != nil {
		t.Fatal(err)
	}
	s.db.SetPointCheck(ctx, "system", p.ID, true, "test")

	fake := &fakeSandbox{t: t, want: data, verdict: verify.OK}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: fake}
	go srv.Serve(ln)
	s.db.SetJobState(ctx, "system", j.ID, "running", "test", "")
	s.db.SetJobState(ctx, "system", j.ID, "ok", "test", "")
	run := func() store.RestoreTest {
		if err := s.queueDueTests(ctx, clock); err != nil {
			t.Fatal(err)
		}
		qs, _ := s.db.QueuedJobs(ctx)
		if len(qs) != 1 || qs[0].Kind != "restore_test" {
			t.Fatalf("queued %+v", qs)
		}
		s.runTest(ctx, qs[0])
		if err := s.queueDueTests(ctx, time.Now()); err != nil { // not due again within the interval
			t.Fatal(err)
		}
		if qs, _ := s.db.QueuedJobs(ctx); len(qs) != 0 {
			t.Fatalf("a test queued again within the interval: %+v", qs)
		}
		ts, _ := s.db.RestoreTests(ctx, vm.ID, 1)
		return ts[0]
	}

	rt := run()
	if rt.Verdict != "OK" || rt.ProvenRTOS == nil || rt.BundleID == "" || !strings.Contains(rt.Reason, "every check passed") {
		t.Fatalf("test %+v", rt)
	}
	if g := fake.got; len(g.Disks) != 2 || g.Disks[0].Target != "vda" || g.Config.Template != "linux-boot" || !strings.HasPrefix(g.Config.Machine.MAC, "52:54:00:") || g.RestoreS <= 0 {
		t.Fatalf("request to the helper %+v", g)
	}
	if left, _ := os.ReadDir(s.cfg.SandboxImagesDir); len(left) != 0 {
		t.Fatalf("restored disks left behind: %v", left)
	}
	anchor, _ := evidence.GenesisKey(s.cfg.ledgerPath())
	if lr, err := evidence.VerifyLedger(s.cfg.ledgerPath(), s.cfg.bundlesDir(), anchor); err != nil || !lr.Intact {
		t.Fatalf("ledger after the test: %+v %v", lr, err)
	}
	if v := statusOf(); v.Status != "OK" || *v.ProvenRTO != *rt.ProvenRTOS {
		t.Fatalf("after a passing test: %+v", v)
	}

	// The guest stops booting: the next test is CRITICAL, and so is the VM.
	clock = clock.Add(2 * time.Hour)
	fake.verdict = verify.Critical
	if rt := run(); rt.Verdict != "CRITICAL" || rt.ProvenRTOS != nil || !strings.Contains(rt.Reason, "no login prompt") {
		t.Fatalf("failing test %+v", rt)
	}
	if v := statusOf(); v.Status != "CRITICAL" || !strings.Contains(v.Reasons[0].Text, "no login prompt") || v.Reasons[0].Code != status.TestFailedBoot {
		t.Fatalf("after a failing test: %+v", v)
	}

	// The helper is gone: the test could not run (UNKNOWN, job failed); that changes nothing about the CRITICAL.
	srv.Close()
	clock = clock.Add(2 * time.Hour)
	if rt := run(); rt.Verdict != "UNKNOWN" || !strings.Contains(rt.Reason, "not tested: sandbox helper") {
		t.Fatalf("unreachable helper %+v", rt)
	}
	v := statusOf()
	if v.Status != "CRITICAL" || len(v.Notes) != 1 || !strings.Contains(v.Notes[0], "could not run") ||
		v.NoteReasons[0].Code != status.TestNotRun || v.NoteReasons[0].Scope == nil || v.NoteReasons[0].Scope.Kind != status.ScopeSandbox {
		t.Fatalf("after a test that could not run: %+v", v)
	}
	jobs, _ := s.db.Jobs(ctx, vm.ID, 1)
	if jobs[0].State != "failed" {
		t.Fatalf("job of the test that could not run: %+v", jobs[0])
	}
	var hist []string
	changes, _ := s.db.StateChanges(ctx, "vm_status", vm.ID, 0, 100)
	for _, c := range changes {
		hist = append(hist, c.From+">"+c.To)
	}
	if strings.Join(hist, " ") != ">UNKNOWN UNKNOWN>OK OK>CRITICAL" {
		t.Fatalf("status history %v", hist)
	}
	var all []VMStatus
	if f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/status", "", &all); len(all) != 1 || all[0].VM != "web1" || all[0].ChangedAt == "" {
		t.Fatalf("GET /status %+v", all)
	}
	io.Discard.Write(nil)
}

// A host that does not answer (ADR-007 C3): the backup job fails with the cause recorded, and once the RPO target is
// missed the VM is CRITICAL with host_unreachable, scoped to the host, so its VMs are one cause.
func TestHostUnreachableCode(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed() // seed-host is 127.0.0.1:1: nothing listens
	s := f.s
	vmID := f.ids["vms"]
	clock := time.Date(2026, 11, 16, 12, 30, 0, 0, time.UTC) // the seed point is from 12:00; the RPO target is 1 h
	s.now = func() time.Time { return clock }
	pts, err := s.db.Points(ctx, vmID, 10)
	if err != nil || len(pts) == 0 {
		t.Fatalf("seed points: %v %v", pts, err)
	}
	if _, err := s.db.SetPointCheck(ctx, "system", pts[0].ID, true, "seed"); err != nil {
		t.Fatal(err)
	}
	hk, err := hostconn.LoadOrCreateKey(filepath.Join(t.TempDir(), "host-key"), "host")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hk.Signer.PublicKey())))
	if _, err := s.db.PinHostKey(ctx, "admin", f.ids["hosts"], line, hostconn.Fingerprint(hk.Signer.PublicKey())); err != nil {
		t.Fatal(err)
	}
	j, err := s.db.CreateJob(ctx, "system", "backup", &vmID, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	s.runBackup(ctx, j)
	got, _ := s.db.Job(ctx, j.ID)
	ji, _ := s.db.JobInfo(ctx, j.ID)
	var d BackupDetail
	if err := json.Unmarshal(ji.Detail, &d); err != nil || got.State != "failed" || !d.Unreachable {
		t.Fatalf("backup of an unreachable host: %+v %s %v", got, ji.Detail, err)
	}
	r, err := s.refreshStatus(ctx, vmID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(r.Reasons, func(x status.Reason) bool { return x.Code == status.HostUnreachable }) {
		t.Fatalf("host_unreachable while the RPO target still holds: %+v", r.Reasons)
	}
	clock = clock.Add(time.Hour)
	if r, err = s.refreshStatus(ctx, vmID); err != nil {
		t.Fatal(err)
	}
	x := r.Reasons[0]
	if r.Status != status.Critical || x.Code != status.HostUnreachable || x.Scope == nil || x.Scope.Name != "seed-host" ||
		x.Params.Job != j.ID || !strings.Contains(x.Text, "host seed-host does not answer") {
		t.Fatalf("after the RPO target: %s %+v", r.Status, r.Reasons)
	}
}
