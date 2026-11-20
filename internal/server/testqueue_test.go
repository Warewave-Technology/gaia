package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

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

	// One ends: the next in the order starts.
	release <- struct{}{}
	for range 100 {
		if n := func() int { k := 0; s.testing.Range(func(_, _ any) bool { k++; return true }); return k }(); n == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := s.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	wait(4)
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
