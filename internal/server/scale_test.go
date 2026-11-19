package server

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
	"github.com/Warewave-Technology/gaia/lab/scale/catalog"
)

// The T24 gate (b) at full size, a reduced size in make check (CLAUDE.md rule 15):
//
//	go test ./internal/server -run TestScaleSimulation -timeout 2h -scale-vms 5000 -scale-hosts 100 -scale-points 3000000 -scale-hours 24 -scale-out sim.json
var (
	scaleVMs    = flag.Int("scale-vms", 200, "TestScaleSimulation: VMs of the synthetic catalog")
	scaleHosts  = flag.Int("scale-hosts", 10, "TestScaleSimulation: hosts")
	scalePoints = flag.Int("scale-points", 12000, "TestScaleSimulation: points")
	scaleHours  = flag.Int("scale-hours", 6, "TestScaleSimulation: simulated hours")
	scaleOut    = flag.String("scale-out", "", "TestScaleSimulation: write the result here (JSON)")
)

// SimResult is what TestScaleSimulation measured.
type SimResult struct {
	VMs, Hosts, Points int
	Hours              int
	Catalog            catalog.Summary
	Events             map[string]int
	StatusChanges      int
	FullRecomputes     []RecomputeStats
	Checks             []StatusCheck
	WallSeconds        float64
	Verdict            string
}

// The status table equals the status model for every VM after a simulated run of the scheduler's clock with events
// (ADR-007 C1, T24 gate b): backups at each policy's interval, restore tests at the test interval, a host that stops
// answering and comes back, damaged contents found, a sandbox outage, a policy made stricter. Events take the
// product's own paths (store writes, then refreshStatus as the job runners do); the clock moves a minute at a time
// and every minute is a scheduler tick (refreshStatuses: the due VMs, and all of them once an hour).
func TestScaleSimulation(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	ctx := context.Background()
	wall := time.Now()
	f := newFixture(t)
	s := f.s
	start := time.Now().UTC().Truncate(time.Minute)
	clock := start
	s.now = func() time.Time { return clock }
	s.db.SetClock(func() time.Time { return clock })
	sum, err := catalog.Fill(ctx, s.db.SQL(), catalog.Options{VMs: *scaleVMs, Hosts: *scaleHosts, Points: *scalePoints, Now: start, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	res := SimResult{VMs: sum.VMs, Hosts: sum.Hosts, Points: sum.Points, Hours: *scaleHours, Catalog: sum, Events: map[string]int{}}

	type simVM struct {
		vm         store.VM
		pol        *store.Policy
		lastBackup time.Time
		lastTest   time.Time
		point      int64 // the newest checked point
		repo       int64
	}
	vms, err := s.db.VMs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	pols := map[int64]store.Policy{}
	ps, err := s.db.Policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		pols[p.ID] = p
	}
	hosts, err := s.db.Hosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(3, 5))
	sim := make([]*simVM, 0, len(vms))
	for _, vm := range vms {
		v := &simVM{vm: vm}
		if vm.PolicyID != nil {
			p := pols[*vm.PolicyID]
			v.pol, v.repo = &p, p.RepositoryID
			// Spread the schedules: the catalog's last backup and test are some time back.
			v.lastBackup = start.Add(-time.Duration(rng.Int64N(p.BackupIntervalS)) * time.Second)
			v.lastTest = start.Add(-time.Duration(rng.Int64N(p.TestIntervalS)) * time.Second)
		}
		if lp, err := s.db.LatestPoint(ctx, vm.ID); err == nil {
			v.point = lp.ID
		}
		sim = append(sim, v)
	}
	downHost := hosts[min(3, len(hosts)-1)].ID // stops answering for hours 2 to 5
	tick := func() {
		t.Helper()
		if err := s.refreshStatuses(ctx, clock); err != nil {
			t.Fatalf("tick at %v: %v", clock, err)
		}
		s.recomputed.mu.Lock()
		if st := s.recomputed.last; st != nil && st.At == clock.Format(time.RFC3339Nano) {
			res.FullRecomputes = append(res.FullRecomputes, *st)
		}
		s.recomputed.mu.Unlock()
	}
	check := func() {
		t.Helper()
		c, err := s.checkStatuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		res.Checks = append(res.Checks, c)
		if len(c.Differ) > 0 || c.Due > 0 || c.Equal+c.Moved != c.VMs {
			b, _ := json.MarshalIndent(c.Differ[:min(3, len(c.Differ))], "", "  ")
			t.Errorf("at %v: %d VMs, %d equal, %d due, %d differ: %s", clock, c.VMs, c.Equal, c.Due, len(c.Differ), b)
		}
	}
	backup := func(v *simVM) {
		t.Helper()
		id := v.vm.ID
		j, err := s.db.CreateJob(ctx, "system", "backup", &id, "schedule (simulation)")
		must0(t, err)
		_, err = s.db.SetJobState(ctx, "system", j.ID, "running", "worker started", "")
		must0(t, err)
		hour := clock.Sub(start).Hours()
		if v.vm.HostID == downHost && hour >= 2 && hour < 5 {
			must0(t, s.db.SetJobDetail(ctx, j.ID, &BackupDetail{Disks: []DiskDetail{}, Unreachable: true}))
			_, err = s.db.SetJobState(ctx, "system", j.ID, "failed", "", "SSH to gaia-backup@host: dial tcp: i/o timeout")
			must0(t, err)
			res.Events["backup_unreachable"]++
		} else {
			p, err := s.db.CreatePoint(ctx, "system", store.Point{VMID: id, JobID: j.ID, RepositoryID: v.repo, Kind: "incremental",
				TakenAt: clock.Format(time.RFC3339Nano), Bytes: 1 << 30})
			must0(t, err)
			_, err = s.db.SetPointCheck(ctx, "system", p.ID, true, "post-backup check passed")
			must0(t, err)
			must0(t, s.db.SetJobDetail(ctx, j.ID, &BackupDetail{Kind: "incremental", Point: p.ID, Disks: []DiskDetail{}}))
			_, err = s.db.SetJobState(ctx, "system", j.ID, "ok", "point "+fmt.Sprint(p.ID), "")
			must0(t, err)
			v.point = p.ID
			res.Events["backup"]++
		}
		_, err = s.refreshStatus(ctx, id)
		must0(t, err)
	}
	test := func(v *simVM) {
		t.Helper()
		if v.point == 0 {
			return
		}
		id := v.vm.ID
		j, err := s.db.CreateJob(ctx, "system", "restore_test", &id, "schedule (simulation)")
		must0(t, err)
		rt, err := s.db.CreateRestoreTest(ctx, "system", store.RestoreTest{VMID: id, PointID: v.point, JobID: j.ID})
		must0(t, err)
		hour := clock.Sub(start).Hours()
		d := &TestDetail{Point: v.point, Test: rt.ID, Disks: []RestoredDisk{}, Checks: []verify.CheckResult{}, Timings: map[string]float64{}, Errors: []string{}}
		verdict, reason := "OK", "every check passed"
		var rto *float64
		switch x := rng.Float64(); {
		case hour >= 1 && hour < 2: // the sandbox helper is stopped
			verdict, reason, d.SandboxDown = "UNKNOWN", "not tested: sandbox helper at /run/gaia-sandbox/sandbox.sock: connect: connection refused", true
			res.Events["test_not_run"]++
		case x < 0.02:
			verdict, reason = "CRITICAL", "boot CRITICAL: no login prompt within 300 s"
			d.Checks = append(d.Checks, verify.CheckResult{Name: "boot", Verdict: "CRITICAL"})
			res.Events["test_critical"]++
		default:
			r := 30 + rng.Float64()*600
			rto = &r
			d.Checks = append(d.Checks, verify.CheckResult{Name: "boot", Verdict: "OK"})
			res.Events["test_ok"]++
		}
		d.Verdict = verdict
		_, err = s.db.FinishRestoreTest(ctx, "system", rt.ID, verdict, rto, reason, "", d)
		must0(t, err)
		_, err = s.refreshStatus(ctx, id)
		must0(t, err)
	}

	tick() // the server's start: every VM
	check()
	end := start.Add(time.Duration(*scaleHours) * time.Hour)
	damaged, stricter := false, false
	for clock.Before(end) {
		clock = clock.Add(time.Minute)
		hour := clock.Sub(start).Hours()
		for _, v := range sim {
			if v.pol == nil {
				continue
			}
			if clock.Sub(v.lastBackup) >= time.Duration(v.pol.BackupIntervalS)*time.Second {
				v.lastBackup = clock
				backup(v)
			}
			if clock.Sub(v.lastTest) >= time.Duration(v.pol.TestIntervalS)*time.Second {
				v.lastTest = clock
				test(v)
			}
		}
		if hour >= 3 && !damaged { // a verification finds damaged contents: some VMs' present points fail their check
			damaged = true
			for _, v := range sim[:min(5, len(sim))] {
				if v.point == 0 {
					continue
				}
				_, err := s.db.SetPointCheck(ctx, "system", v.point, false, "repository verification (simulation): damaged contents")
				must0(t, err)
				_, err = s.refreshStatus(ctx, v.vm.ID)
				must0(t, err)
				res.Events["damaged"]++
			}
		}
		if hour >= 4 && !stricter { // a policy made stricter, as PATCH /policies/{id} does it
			stricter = true
			for _, p := range ps {
				if p.Name == "std-linux" {
					rpo := p.RPOTargetS / 2
					_, err := s.db.UpdatePolicy(ctx, p.ID, store.PolicyPatch{RPOTargetS: &rpo})
					must0(t, err)
					must0(t, s.db.MarkStatusesDue(ctx, 0, p.ID))
					res.Events["policy_change"]++
				}
			}
		}
		tick()
		if clock.Sub(start)%(6*time.Hour) == 0 {
			check()
		}
	}
	if clock.Sub(start)%(6*time.Hour) != 0 {
		check()
	}
	n, err := countStatusChanges(ctx, s.db)
	must0(t, err)
	res.StatusChanges = n
	res.WallSeconds = time.Since(wall).Seconds()
	res.Verdict = status.OK
	if t.Failed() {
		res.Verdict = status.Critical
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if *scaleOut != "" {
		if err := os.WriteFile(*scaleOut, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d VMs, %d h: events %v, %d VM status changes, %d full recomputes, checks %d, %.1f s", res.VMs, res.Hours, res.Events,
		res.StatusChanges, len(res.FullRecomputes), len(res.Checks), res.WallSeconds)
	if res.Events["backup_unreachable"] == 0 && *scaleHours >= 5 || res.Events["test_not_run"] == 0 && *scaleHours >= 2 || res.Events["backup"] == 0 {
		t.Errorf("the simulation did not reach its events: %v", res.Events)
	}
}

func must0(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func countStatusChanges(ctx context.Context, db *store.DB) (int, error) {
	var n int
	err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM state_changes WHERE entity = 'vm_status'`).Scan(&n)
	return n, err
}
