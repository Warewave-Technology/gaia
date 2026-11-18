package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// A failed point keeps its VM CRITICAL until it is pruned (ADR-007 B4.3); an admin can forget it, with a reason, and
// only a failed one. Its diskstore point is deleted, its row pruned with the reason, and the status no longer names it.
func TestForgetPoint(t *testing.T) {
	ctx := context.Background()
	f, repo, vms, pts, data := quarantineFixture(t)
	if v := runVerification(t, f.s, repo, "full"); v.State != "critical" {
		t.Fatalf("verification %+v", v)
	}
	ok := addPoint(t, f.s, repo, vms[0], data) // the next backup: checked and not failed
	statusOf := func() string {
		var st json.RawMessage
		if code := f.do(f.tokens[store.RoleAdmin], "GET", fmt.Sprintf("/api/v1/vms/%d/status", vms[0].ID), "", &st); code != 200 {
			t.Fatalf("status: %d", code)
		}
		return string(st)
	}
	if s := statusOf(); !strings.Contains(s, "integrity_failed") {
		t.Fatalf("status before: %s", s)
	}
	forget := func(token string, id int64, body string) int {
		return f.do(token, "POST", fmt.Sprintf("/api/v1/points/%d/forget", id), body, nil)
	}
	for _, c := range []struct {
		token string
		id    int64
		body  string
		want  int
	}{
		{f.tokens[store.RoleOperator], pts[0].ID, `{"reason":"damaged"}`, 403},
		{f.tokens[store.RoleAuditor], pts[0].ID, `{"reason":"damaged"}`, 403},
		{f.tokens[store.RoleAdmin], pts[0].ID, `{"reason":"  "}`, 400},
		{f.tokens[store.RoleAdmin], ok.ID, `{"reason":"not damaged"}`, 409},
		{f.tokens[store.RoleAdmin], 9999, `{"reason":"no such point"}`, 404},
	} {
		if got := forget(c.token, c.id, c.body); got != c.want {
			t.Errorf("forget point %d with %s: %d, want %d", c.id, c.body, got, c.want)
		}
	}
	disks, _ := f.s.db.PointDisks(ctx, pts[0].ID)
	if got := forget(f.tokens[store.RoleAdmin], pts[0].ID, `{"reason":"storage fault of the test; point 3 is newer"}`); got != 200 {
		t.Fatalf("forget: %d", got)
	}
	p, _ := f.s.db.Point(ctx, pts[0].ID)
	changes, _ := f.s.db.StateChanges(ctx, "point", p.ID, 0, 10)
	if p.State != "pruned" || len(changes) == 0 || changes[len(changes)-1].Reason != "forgotten by admin: storage fault of the test; point 3 is newer" {
		t.Fatalf("point %+v, changes %+v", p, changes)
	}
	st, err := f.s.OpenRepository(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Point(ctx, disks[0].StoreID); err == nil {
		t.Fatal("the forgotten point's diskstore point is still there")
	}
	st.Close(ctx)
	if s := statusOf(); strings.Contains(s, "integrity_failed") {
		t.Fatalf("status after: %s", s)
	}
	audit, _ := f.s.db.Audit(ctx, 0, 1000)
	if a := audit[len(audit)-1]; !strings.Contains(a.Target, fmt.Sprintf("/points/%d", p.ID)) || !strings.Contains(a.Detail, "forgotten: storage fault") {
		t.Fatalf("audit %+v", a)
	}
	if got := forget(f.tokens[store.RoleAdmin], pts[0].ID, `{"reason":"again"}`); got != 409 {
		t.Fatalf("forgetting a pruned point: %d", got)
	}
}

// What a stop leaves running is ended at the next start: jobs fail, a restore test becomes UNKNOWN, a verification
// fails (one left running would block its repository's verifications), and a backup or restore test is queued again,
// but not a second time.
func TestFinishInterrupted(t *testing.T) {
	ctx := context.Background()
	f, repo, vms, pts, _ := quarantineFixture(t)
	db := f.s.db
	running := func(kind string, vm int64, reason string) store.Job {
		j, err := db.CreateJob(ctx, "system", kind, &vm, reason)
		if err != nil {
			t.Fatal(err)
		}
		if j, err = db.SetJobState(ctx, "system", j.ID, "running", "worker started", ""); err != nil {
			t.Fatal(err)
		}
		return j
	}
	if qs, _ := db.QueuedJobs(ctx); len(qs) != 2 { // addPoint's jobs: web2's stays queued, so its retry is not queued
		t.Fatalf("queued %+v", qs)
	}
	j1, _ := db.QueuedJobs(ctx)
	if _, err := db.SetJobState(ctx, "system", j1[0].ID, "cancelled", "test", ""); err != nil {
		t.Fatal(err)
	}
	backup := running("backup", vms[0].ID, "schedule")
	test := running("restore_test", vms[0].ID, "schedule")
	rt, err := db.CreateRestoreTest(ctx, "system", store.RestoreTest{VMID: vms[0].ID, PointID: pts[0].ID, JobID: test.ID})
	if err != nil {
		t.Fatal(err)
	}
	again := running("restore_test", vms[1].ID, fmt.Sprintf("%s%d: the server stopped while it ran", retryReason, test.ID))
	ver, err := db.StartVerification(ctx, "system", repo.ID, "full", "schedule")
	if err != nil {
		t.Fatal(err)
	}

	if err := f.s.finishInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	for _, j := range []store.Job{backup, test, again} {
		got, _ := db.Job(ctx, j.ID)
		if got.State != "failed" || got.Error != "the server stopped while it ran" {
			t.Errorf("job %d: %+v", j.ID, got)
		}
	}
	if got, _ := db.RestoreTest(ctx, rt.ID); got.Verdict != "UNKNOWN" || got.FinishedAt == "" || got.Reason != "not tested: the server stopped while it ran" {
		t.Errorf("restore test %+v", got)
	}
	if got, _ := db.Verification(ctx, ver.ID); got.State != "failed" || got.Error != "the server stopped while it ran" {
		t.Errorf("verification %+v", got)
	}
	qs, _ := db.QueuedJobs(ctx)
	var queued []string
	for _, q := range qs {
		queued = append(queued, fmt.Sprintf("%s vm %d: %s", q.Kind, *q.VMID, q.Reason))
	}
	want := []string{
		fmt.Sprintf("backup vm %d: test", vms[1].ID),
		fmt.Sprintf("backup vm %d: retry of job %d: the server stopped while it ran", vms[0].ID, backup.ID),
		fmt.Sprintf("restore_test vm %d: retry of job %d: the server stopped while it ran", vms[0].ID, test.ID),
	}
	if strings.Join(queued, "\n") != strings.Join(want, "\n") {
		t.Fatalf("queued:\n%s\nwant:\n%s", strings.Join(queued, "\n"), strings.Join(want, "\n"))
	}
	// The scheduler verifies the repository again (a running verification would have stopped it).
	if err := f.s.startDueVerifications(ctx, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if vs, _ := db.Verifications(ctx, repo.ID, 1); len(vs) != 1 || vs[0].ID == ver.ID {
		t.Fatalf("no new verification: %+v", vs)
	}
	f.s.jobsWG.Wait()
}

// A stop waits for running jobs up to stop_drain, then ends them through the jobs' context.
func TestDrain(t *testing.T) {
	for _, c := range []struct {
		name   string
		job    time.Duration // how long the job runs unless its context ends
		drain  time.Duration
		cancel bool // the job's context ended before the job did
	}{
		{"the job ends within the drain", 50 * time.Millisecond, 10 * time.Second, false},
		{"the drain ends the job", time.Hour, 100 * time.Millisecond, true},
		{"no drain", time.Hour, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			s := f.s
			ended := make(chan bool, 1)
			s.running.Store(int64(1), int64(1))
			s.jobsWG.Add(1)
			go func() {
				defer s.jobsWG.Done()
				defer s.running.Delete(int64(1))
				select {
				case <-time.After(c.job):
					ended <- false
				case <-s.jobs.Done():
					ended <- true
				}
			}()
			start := time.Now()
			s.drain(c.drain)
			took := time.Since(start)
			if got := <-ended; got != c.cancel {
				t.Fatalf("cancelled %v, want %v", got, c.cancel)
			}
			if c.cancel && took < c.drain || took > c.drain+5*time.Second {
				t.Fatalf("drain took %s (stop_drain %s)", took, c.drain)
			}
		})
	}
}

// A backup or restore test a stop ended (the drain was over) records its own failure; it is queued again for the next
// start, but only after a stop, only once, and not when one is queued already.
func TestRetryIfStopped(t *testing.T) {
	ctx := context.Background()
	f, _, vms, _, _ := quarantineFixture(t)
	db := f.s.db
	for _, q := range must(db.QueuedJobs(ctx)) { // addPoint's jobs
		db.SetJobState(ctx, "system", q.ID, "cancelled", "test", "")
	}
	failed := func(vm int64, reason string) store.Job {
		j, _ := db.CreateJob(ctx, "system", "restore_test", &vm, reason)
		db.SetJobState(ctx, "system", j.ID, "running", "worker started", "")
		j, _ = db.SetJobState(ctx, "system", j.ID, "failed", "restore test", "context canceled")
		return j
	}
	queued := func() int { return len(must(db.QueuedJobs(ctx))) }
	j := failed(vms[0].ID, "schedule")
	f.s.retryIfStopped(j) // no stop: a failure of its own
	if n := queued(); n != 0 {
		t.Fatalf("queued without a stop: %d", n)
	}
	f.s.stopJobs()
	f.s.retryIfStopped(j)
	f.s.retryIfStopped(j) // its VM has one queued now
	f.s.retryIfStopped(failed(vms[1].ID, fmt.Sprintf("%s1: the server stopped while it ran", retryReason)))
	qs := must(db.QueuedJobs(ctx))
	if len(qs) != 1 || *qs[0].VMID != vms[0].ID || qs[0].Reason != fmt.Sprintf("retry of job %d: the server stopped while it ran", j.ID) {
		t.Fatalf("queued %+v", qs)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
