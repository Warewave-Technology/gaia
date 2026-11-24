package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// A helper that only answers GET /v1/capacity, with the given slots.
func capacityHelper(t *testing.T, s *Server, slots int) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gtq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(sandbox.Capacity{Slots: slots, MemoryLimitMiB: 8192, OverheadMiB: sandbox.OverheadMiB, Running: []sandbox.Running{}, Waiting: []sandbox.Waiting{}})
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	s.sandbox = sandbox.NewClient(sock)
}

// As many tests run at once as the helper has slots, one per VM, chosen in C4's order; when one ends, the next one
// in the order starts. The capacity and the queue views say the same order; a what-if saves nothing and is not audited.
func TestParallelTestDispatch(t *testing.T) {
	ctx := context.Background()
	f, ids, now := fleetFixture(t)
	s := f.s
	capacityHelper(t, s, 3)
	var mu sync.Mutex
	var started []string
	release := make(chan struct{})
	var once sync.Once
	done := func() { once.Do(func() { close(release) }) }
	t.Cleanup(done) // a failed test must not leave its fake tests waiting
	s.testRun = func(ctx context.Context, j store.Job) {
		s.db.SetJobState(context.Background(), "system", j.ID, "running", "test", "") // as runTest does first
		vm, _ := s.db.VM(context.Background(), *j.VMID)
		mu.Lock()
		started = append(started, vm.Name)
		mu.Unlock()
		<-release
		s.db.SetJobState(context.Background(), "system", j.ID, "ok", "test", "")
	}
	// Queued tests of five VMs: app-kvm-a is CRITICAL (failed to boot), new-1 never proven, web-1..3 OK with proofs
	// ending at different times (their stored proof is 20 minutes old; web-3's ends first).
	for i, name := range []string{"web-1", "web-2", "new-1", "web-3", "app-kvm-a"} {
		id := ids[name]
		if _, err := s.db.CreateJob(ctx, "system", "restore_test", &id, "test"); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	pf := *unixOf(now.Add(-time.Hour)) // web-3's proof is older: it expires first
	if _, err := s.db.SQL().ExecContext(ctx, `UPDATE vm_status SET proven_finished_at = ? WHERE vm_id = ?`, pf, ids["web-3"]); err != nil {
		t.Fatal(err)
	}
	// The order: CRITICAL retest, never proven, earliest proof expiry, then by policy and queue time.
	qs, err := s.db.QueuedTests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, j := range qs {
		vm, _ := s.db.VM(ctx, *j.VMID)
		order = append(order, vm.Name)
	}
	if fmt.Sprint(order) != "[app-kvm-a new-1 web-3 web-1 web-2]" {
		t.Fatalf("queue order %v", order)
	}
	if err := s.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	wait := func(n int) {
		t.Helper()
		for range 200 {
			mu.Lock()
			k := len(started)
			mu.Unlock()
			if k >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("started %v, want %d", started, n)
	}
	wait(3)
	time.Sleep(20 * time.Millisecond) // nothing more starts
	mu.Lock()
	got := append([]string{}, started...)
	mu.Unlock()
	slices.Sort(got)
	if fmt.Sprint(got) != "[app-kvm-a new-1 web-3]" {
		t.Fatalf("started %v, want the first three of the order", got)
	}

	// The queue view: the two left, in order, with estimated starts.
	var q TestQueueView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/test-queue", "", &q); st != 200 || q.Slots != 3 || q.Total != 2 {
		t.Fatalf("queue: %d %+v", st, q)
	}
	// (The estimated starts count the VMs whose next test is due now too: the scheduler queues them at its next tick.)
	if q.Items[0].Position != 1 || q.Items[1].Position != 2 || q.Items[0].EstimatedStart.IsZero() || q.Items[0].EstimatedStart.After(q.Items[1].EstimatedStart) {
		t.Fatalf("queue items %+v", q.Items)
	}
	var cv CapacityView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/capacity", "", &cv); st != 200 || cv.Slots != 3 || cv.VMs != 11 || cv.Sandbox == nil {
		t.Fatalf("capacity: %d slots %d VMs %d sandbox %v", st, cv.Slots, cv.VMs, cv.Sandbox)
	}
	audits := func() int {
		var n int
		s.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&n)
		return n
	}
	before := audits()
	var wi CapacityView
	if st := f.do(f.tokens[store.RoleAuditor], "POST", "/api/v1/capacity/what-if", `{"slots":0}`, &wi); st != 200 || wi.Slots != 0 || wi.Coverage != 0 || wi.WhatIf == nil {
		t.Fatalf("what-if: %d slots %d coverage %v", st, wi.Slots, wi.Coverage)
	}
	if after := audits(); after != before {
		t.Fatalf("a what-if was audited: %d -> %d entries", before, after)
	}
	if st := f.do(f.tokens[store.RoleAuditor], "POST", "/api/v1/capacity/what-if", fmt.Sprintf(`{"policies":[{"policy_id":%d,"test_interval_s":30}]}`, f.ids["policies"]), nil); st != 400 {
		t.Fatalf("a what-if with a 30 s interval: %d", st)
	}

	// One ends: the next in the order starts at once, not at the next tick (which never comes here).
	wctx, stop := context.WithCancel(ctx)
	waited := make(chan bool)
	go func() { waited <- s.waitTick(wctx, nil) }()
	release <- struct{}{}
	wait(4)
	stop()
	if <-waited {
		t.Fatal("waitTick saw a tick")
	}
	mu.Lock()
	if started[3] != "web-1" && started[3] != "web-2" {
		mu.Unlock()
		t.Fatalf("fourth %v", started)
	}
	mu.Unlock()
	done()
	s.jobsWG.Wait()
	_ = status.OK
}

// Restores of one repository run beside each other (each opens the repository on its own), not beside a writer: a
// backup, retention, maintenance or verification waits for the restores, and restores that come after a waiting
// writer wait for it.
func TestRestoresShareTheRepository(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	var repo store.Repository
	if st := f.do(f.tokens[store.RoleAdmin], "POST", "/api/v1/repositories", fmt.Sprintf(`{"name":"main","kind":"filesystem","location":%q,"kit_passphrase":%q}`,
		filepath.Join(f.dir, "repo"), kitPassphrase), &repo); st != 201 {
		t.Fatalf("repository: %d", st)
	}
	h, _ := f.s.db.CreateHost(ctx, store.HostInput{Name: "kvm", Address: "kvm", SSHUser: "gaia-backup"})
	vm, err := f.s.db.CreateVM(ctx, "admin", store.VMInput{HostID: h.ID, Name: "web1"})
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 3*diskstore.BlockSize)
	r := rand.New(rand.NewPCG(7, 8))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	p := addPoint(t, f.s, repo, vm, data)
	want := fmt.Sprintf("%x", sha256.Sum256(data))

	lock := f.s.repoLocks.get(repo.ID)
	lock.RLock() // a restore that is running
	type result struct {
		disks []RestoredDisk
		err   error
	}
	res := make(chan result, 3)
	for range 3 {
		go func() { d, err := f.s.restoreCheck(ctx, p); res <- result{d, err} }()
	}
	for range 3 {
		select {
		case x := <-res:
			if x.err != nil || len(x.disks) != 1 || x.disks[0].SHA256 != want {
				t.Fatalf("a restore beside other restores: %+v %v", x.disks, x.err)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("a restore waited for another restore")
		}
	}
	wrote := make(chan struct{})
	go func() { lock.Lock(); close(wrote); lock.Unlock() }() // a backup
	time.Sleep(50 * time.Millisecond)
	late := make(chan error, 1)
	go func() { _, err := f.s.restoreCheck(ctx, p); late <- err }()
	select {
	case <-wrote:
		t.Fatal("a writer ran beside a restore")
	case <-late:
		t.Fatal("a restore went before a waiting writer")
	case <-time.After(100 * time.Millisecond):
	}
	lock.RUnlock()
	<-wrote
	if err := <-late; err != nil {
		t.Fatal(err)
	}
}

// Two tests whose disks do not fit the images directory together (T29: the Windows guest and gaia-t23-uefi on the
// lab, 4 slots): the second waits for the first, then runs; none is "not tested". The queue view shows the wait.
func TestDispatchWaitsForImages(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.seed()
	s := f.s
	if _, err := s.db.SQL().ExecContext(ctx, `UPDATE jobs SET state = 'ok' WHERE state IN ('queued', 'running')`); err != nil { // only this test's
		t.Fatal(err)
	}
	s.cfg.SandboxImagesDir = t.TempDir()
	capacityHelper(t, s, 4)
	free, err := freeBytes(s.cfg.SandboxImagesDir)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := s.db.CreateHost(ctx, store.HostInput{Name: "kvm", Address: "kvm", SSHUser: "gaia-backup"})
	var vms []store.VM
	for _, name := range []string{"win", "uefi"} {
		vm, err := s.db.CreateVM(ctx, "admin", store.VMInput{HostID: h.ID, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		bj, err := s.db.CreateJob(ctx, "system", "backup", &vm.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		p, err := s.db.CreateBackupPoint(ctx, "system", store.Point{VMID: vm.ID, JobID: bj.ID, RepositoryID: f.ids["repositories"], Kind: "full",
			Checkpoint: "c", TakenAt: time.Now().UTC().Format(time.RFC3339Nano), Machine: json.RawMessage(`{}`)},
			[]store.PointDisk{{Target: "vda", SizeBytes: free / 10 * 6, BlockSize: 4 << 20, StoreID: "k" + name}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.SQL().ExecContext(ctx, `UPDATE points SET check_state = 'ok' WHERE id = ?`, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.CreateJob(ctx, "system", "restore_test", &vm.ID, "test"); err != nil {
			t.Fatal(err)
		}
		vms = append(vms, vm)
	}
	if _, err := s.db.SQL().ExecContext(ctx, `UPDATE jobs SET state = 'ok' WHERE kind = 'backup' AND state = 'queued'`); err != nil { // made the points
		t.Fatal(err)
	}
	var mu sync.Mutex
	var started []int64
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	s.testRun = func(ctx context.Context, j store.Job) { // as runTest: running, the restore's reservation, then ok
		s.db.SetJobState(context.Background(), "system", j.ID, "running", "test", "")
		r, err := s.reserveImages(j.ID, free/10*6)
		if err != nil {
			t.Errorf("job %d: %v", j.ID, err)
			return
		}
		mu.Lock()
		started = append(started, *j.VMID)
		mu.Unlock()
		<-release
		r()
		s.db.SetJobState(context.Background(), "system", j.ID, "ok", "test", "")
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(started) }
	if err := s.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200 && count() < 1; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if n := count(); n != 1 {
		t.Fatalf("%d tests started, want 1: the second does not fit beside the first", n)
	}
	var q TestQueueView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/test-queue", "", &q); st != 200 || len(q.WaitingForImages) != 1 ||
		q.WaitingForImages[0].NeedBytes != free/10*6 || q.WaitingForImages[0].VM == "" {
		t.Fatalf("queue view: %d %+v", st, q.WaitingForImages)
	}
	var cv CapacityView
	if st := f.do(f.tokens[store.RoleAuditor], "GET", "/api/v1/views/capacity", "", &cv); st != 200 || cv.Images.HeldBytes != free/10*6 ||
		cv.Images.RoomBytes < cv.Images.FreeBytes || len(cv.TooLarge) != 0 || cv.ImagesRoomBytes != cv.Images.RoomBytes {
		t.Fatalf("capacity view: %d %+v room %d too large %+v", st, cv.Images, cv.ImagesRoomBytes, cv.TooLarge)
	}
	// The first ends: the second starts (here at the next dispatch; the server's slotFreed makes it at once).
	release <- struct{}{}
	for i := 0; i < 200; i++ {
		if err := s.dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		if count() == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	got := append([]int64{}, started...)
	mu.Unlock()
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("started %v, want both VMs %d and %d", got, vms[0].ID, vms[1].ID)
	}
	close(release)
	s.jobsWG.Wait()
	if held, waiting := s.imagesHeld(); held != 0 || len(waiting) != 0 {
		t.Fatalf("after both: held %d waiting %+v", held, waiting)
	}
}
