package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/sandbox"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/verify"
)

// queueDueTests queues a restore test for every VM with a policy whose last restore-test job is at least the
// policy's test interval old and that has a checked point to test.
func (s *Server) queueDueTests(ctx context.Context, now time.Time) error {
	due, err := s.db.ScheduleFacts(ctx, "restore_test")
	if err != nil {
		return err
	}
	for _, f := range due {
		if f.Open || !f.HasPoint { // one at a time per VM; nothing to test yet
			continue
		}
		if f.LastCreatedAt != "" {
			t, err := time.Parse(time.RFC3339Nano, f.LastCreatedAt)
			if err != nil {
				return err
			}
			if now.Sub(t) < time.Duration(f.TestIntervalS)*time.Second-tickSlack {
				continue
			}
		}
		id := f.VMID
		if _, err := s.db.CreateJob(ctx, "system", "restore_test", &id, fmt.Sprintf("schedule: policy %s, every %d s", f.Policy, f.TestIntervalS)); err != nil {
			return err
		}
	}
	return nil
}

// TestDetail is a restore-test job's summary (jobs.detail).
type TestDetail struct {
	Point        int64                `json:"point"`
	Test         int64                `json:"test"`
	Machine      verify.Machine       `json:"machine"`
	MACGenerated bool                 `json:"mac_generated,omitempty"` // the VM has no interface: a MAC derived from its ID
	Disks        []RestoredDisk       `json:"disks"`
	RestoreS     float64              `json:"restore_s"`
	Verdict      string               `json:"verdict"`
	Checks       []verify.CheckResult `json:"checks"`
	Timings      map[string]float64   `json:"timings_s"`
	Errors       []string             `json:"errors"`
	Bundle       string               `json:"bundle"`
	LedgerSeq    int                  `json:"ledger_seq"`
	SandboxDown  bool                 `json:"sandbox_down,omitempty"` // not tested: the sandbox helper did not answer
}

// runTest runs one queued restore-test job. The job is ok when the test ran to a verdict, whatever the verdict;
// it fails when no verdict could be reached (the test then stays UNKNOWN, with the reason).
func (s *Server) runTest(ctx context.Context, j store.Job) {
	d := &TestDetail{Disks: []RestoredDisk{}, Checks: []verify.CheckResult{}, Timings: map[string]float64{}, Errors: []string{}}
	err := s.restoreTest(ctx, j, d)
	cctx := context.WithoutCancel(ctx)
	if derr := s.db.SetJobDetail(cctx, j.ID, d); derr != nil {
		s.log.Error("job detail", "job", j.ID, "error", derr)
	}
	to, msg := "ok", ""
	if err != nil {
		to, msg = "failed", err.Error()
		s.log.Error("restore test failed", "job", j.ID, "error", err)
	}
	if _, err := s.db.SetJobState(cctx, "system", j.ID, to, fmt.Sprintf("restore test %d: %s", d.Test, d.Verdict), msg); err != nil {
		s.log.Error("job state", "job", j.ID, "error", err)
	}
	if _, err := s.refreshStatus(cctx, *j.VMID); err != nil {
		s.log.Error("status", "vm", *j.VMID, "error", err)
	}
}

func (s *Server) restoreTest(ctx context.Context, j store.Job, d *TestDetail) error {
	if j.VMID == nil {
		return errors.New("a restore test job without a VM")
	}
	if _, err := s.db.SetJobState(ctx, "system", j.ID, "running", "worker started", ""); err != nil {
		return err
	}
	vm, err := s.db.VM(ctx, *j.VMID)
	if err != nil {
		return err
	}
	if vm.PolicyID == nil {
		return fmt.Errorf("VM %s has no policy (it says which checks to run)", vm.Name)
	}
	pol, err := s.db.Policy(ctx, *vm.PolicyID)
	if err != nil {
		return err
	}
	p, err := s.db.LatestPoint(ctx, vm.ID)
	if err != nil {
		return fmt.Errorf("no checked point to test: %w", err)
	}
	d.Point = p.ID
	t, err := s.db.CreateRestoreTest(ctx, "system", store.RestoreTest{VMID: vm.ID, PointID: p.ID, JobID: j.ID})
	if err != nil {
		return err
	}
	d.Test, d.Verdict = t.ID, verify.Unknown
	verdict, reason, bundle, rto, terr := s.testPoint(ctx, vm, pol, p, t, d)
	if terr != nil {
		verdict, reason = verify.Unknown, "not tested: "+terr.Error()
		d.SandboxDown = errors.Is(terr, sandbox.ErrUnavailable)
	}
	d.Verdict, d.Bundle = verdict, bundle
	if _, err := s.db.FinishRestoreTest(context.WithoutCancel(ctx), "system", t.ID, verdict, rto, reason, bundle, d); err != nil {
		return errors.Join(terr, err)
	}
	s.log.Info("restore test", "vm", vm.Name, "test", t.ID, "point", p.ID, "verdict", verdict, "reason", reason)
	return terr
}

// testPoint restores the point's disks for the sandbox helper, has it boot and check them, and seals the bundle it
// returns. err means no verdict was reached.
func (s *Server) testPoint(ctx context.Context, vm store.VM, pol store.Policy, p store.Point, t store.RestoreTest, d *TestDetail) (verdict, reason, bundle string, rto *float64, err error) {
	cfg := verify.Config{VM: vm.Name, Template: pol.CheckTemplate}
	if len(p.Machine) > 0 {
		dec := json.NewDecoder(bytes.NewReader(p.Machine))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg.Machine); err != nil {
			return "", "", "", nil, fmt.Errorf("point %d: machine: %w", p.ID, err)
		}
	}
	if cfg.Machine.MAC == "" {
		h := sha256.Sum256([]byte(fmt.Sprintf("gaia-vm-%d-%s", vm.ID, vm.UUID)))
		cfg.Machine.MAC, d.MACGenerated = fmt.Sprintf("52:54:00:%02x:%02x:%02x", h[0], h[1], h[2]), true
	}
	if c := bytes.TrimSpace(pol.Checks); len(c) > 0 && !bytes.Equal(c, []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(c))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg.Checks); err != nil {
			return "", "", "", nil, fmt.Errorf("policy %s: checks: %w", pol.Name, err)
		}
	}
	d.Machine = cfg.Machine

	disks, err := s.db.PointDisks(ctx, p.ID)
	if err != nil {
		return "", "", "", nil, err
	}
	var need int64
	for _, k := range disks {
		need += k.SizeBytes
	}
	release, err := s.reserveImages(need)
	if err != nil {
		return "", "", "", nil, err
	}
	defer release()
	var images []verify.DiskImage
	defer func() {
		for _, im := range images {
			if err := os.Remove(im.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.log.Error("removing a restored disk", "file", im.Path, "error", err)
			}
		}
	}()
	restoreS, err := s.restoreDisks(ctx, p, disks, t.ID, &images, d)
	if err != nil {
		return "", "", "", nil, err
	}
	d.RestoreS = restoreS

	dir, res, err := s.sandbox.Verify(ctx, sandbox.Request{Config: cfg, Disks: images, RestoreS: restoreS}, s.cfg.bundlesDir())
	if err != nil {
		if dir != "" {
			os.RemoveAll(dir)
		}
		return "", "", "", nil, err
	}
	d.Checks, d.Timings, d.Errors = res.Checks, res.Timings, res.Errors
	key, err := evidence.LoadPrivateKey(s.cfg.keyPath())
	if err != nil {
		return "", "", "", nil, err
	}
	sum, err := evidence.Seal(dir, res.BundleID, map[string]string{"vm": vm.Name, "verdict": res.Verdict,
		"point": fmt.Sprint(p.ID), "point_taken_at": p.TakenAt, "test": fmt.Sprint(t.ID)}, key)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("sealing %s: %w", res.BundleID, err)
	}
	e, err := evidence.Append(s.cfg.ledgerPath(), evidence.Entry{BundleID: res.BundleID, BundleSHA256: sum, Subject: vm.Name, Verdict: res.Verdict}, key)
	if err != nil {
		// A ledger that refuses an entry is a ledger problem, not only this test's: check it now (a change notifies).
		if _, cerr := s.checkLedger(context.WithoutCancel(ctx), "system", "appending bundle "+res.BundleID+" failed: "+err.Error()); cerr != nil {
			s.log.Error("ledger check", "error", cerr)
		}
		return "", "", "", nil, fmt.Errorf("ledger: %w", err)
	}
	d.LedgerSeq = e.Seq
	tid, vid := t.ID, vm.ID
	if _, err := s.db.AddEvidence(context.WithoutCancel(ctx), store.EvidenceBundle{BundleID: res.BundleID, TestID: &tid, VMID: &vid,
		LedgerSeq: e.Seq, BundleSHA256: sum, Verdict: res.Verdict}); err != nil {
		return "", "", "", nil, err
	}
	if v, ok := res.Timings["proven_rto"]; ok && res.Verdict == verify.OK {
		rto = &v
	}
	return res.Verdict, testReason(res, restoreS), res.BundleID, rto, nil
}

// restoreDisks restores every disk of the point into the sandbox images directory, boot disk (the first target)
// first, and returns how long it took: the restore part of Proven RTO.
func (s *Server) restoreDisks(ctx context.Context, p store.Point, disks []store.PointDisk, testID int64, images *[]verify.DiskImage, d *TestDetail) (float64, error) {
	r, err := s.db.Repository(ctx, p.RepositoryID)
	if err != nil {
		return 0, err
	}
	lock := s.repoLocks.get(r.ID)
	lock.RLock()
	defer lock.RUnlock()
	st, err := s.OpenRepository(ctx, r)
	if err != nil {
		return 0, err
	}
	defer st.Close(ctx)
	start := time.Now()
	for _, k := range disks {
		sp, err := st.Point(ctx, k.StoreID)
		if err != nil {
			return 0, fmt.Errorf("disk %s: %w", k.Target, err)
		}
		path := filepath.Join(s.cfg.SandboxImagesDir, fmt.Sprintf("vm%d-test%d-%s.raw", p.VMID, testID, k.Target))
		*images = append(*images, verify.DiskImage{Path: path, Format: "raw", Target: k.Target})
		t := time.Now()
		if _, err := st.Restore(ctx, sp, path, 4); err != nil {
			return 0, fmt.Errorf("restoring disk %s: %w", k.Target, err)
		}
		fi, err := os.Stat(path)
		if err != nil {
			return 0, err
		}
		d.Disks = append(d.Disks, RestoredDisk{Target: k.Target, Bytes: fi.Size(), Seconds: time.Since(t).Seconds()})
	}
	return time.Since(start).Seconds(), nil
}

// testReason says in one line why a test has its verdict.
func testReason(r *verify.Result, restoreS float64) string {
	if r.Verdict == verify.OK {
		var cs []string
		for _, c := range r.Checks {
			if c.Type != "image_hash" {
				cs = append(cs, fmt.Sprintf("%s %.1f s", c.Name, c.Seconds))
			}
		}
		return fmt.Sprintf("every check passed (%s); proven RTO %.1f s = restore %.1f s + ready %.1f s",
			strings.Join(cs, ", "), r.Timings["proven_rto"], restoreS, r.Timings["ready"])
	}
	var bad []string
	for _, c := range r.Checks {
		if c.Verdict != verify.OK {
			bad = append(bad, fmt.Sprintf("%s %s: %s", c.Name, c.Verdict, c.Detail))
		}
	}
	bad = append(bad, r.Errors...)
	return strings.Join(bad, "; ")
}

// statusInput gathers what a VM's status depends on.
func (s *Server) statusInput(ctx context.Context, vmID int64) (status.Input, error) {
	f, err := s.db.StatusFacts(ctx, vmID)
	if err != nil {
		return status.Input{}, err
	}
	in := status.Input{Now: s.now().UTC(), DamagedPoints: f.Damaged, HostKeyUntrusted: !f.HostKeyTrusted,
		Host: &status.Scope{Kind: status.ScopeHost, ID: f.VM.HostID, Name: f.HostName}}
	if f.Policy != nil {
		in.Targets = &status.Targets{RPOS: f.Policy.RPOTargetS, RTOS: f.Policy.RTOTargetS, MaxProofAgeS: f.Policy.MaxProofAgeS}
		in.Repository = &status.Scope{Kind: status.ScopeRepository, ID: f.RepositoryID, Name: f.RepositoryName}
	}
	if f.NewestChecked != nil {
		tk, err := time.Parse(time.RFC3339Nano, f.NewestChecked.TakenAt)
		if err != nil {
			return in, fmt.Errorf("point %d: taken_at: %w", f.NewestChecked.ID, err)
		}
		in.NewestChecked = &status.Point{ID: f.NewestChecked.ID, TakenAt: tk, FullReason: unplannedFull(*f.NewestChecked)}
	}
	if in.FailedBackup, err = failedBackup(f.LastBackup, f.LastBackupInfo, in.NewestChecked); err != nil {
		return in, err
	}
	if b := in.FailedBackup; b != nil && b.HostKeyUntrusted && f.HostKeyTrusted {
		b.HostKeyUntrusted = false // trusted since the job failed: that cause is gone, the failure stays a failure
	}
	test := func(t *store.RestoreTest, p *store.Point) (*status.Test, error) {
		if t == nil {
			return nil, nil
		}
		tk, err := time.Parse(time.RFC3339Nano, p.TakenAt)
		if err != nil {
			return nil, fmt.Errorf("point %d: taken_at: %w", p.ID, err)
		}
		fin, err := time.Parse(time.RFC3339Nano, t.FinishedAt)
		if err != nil {
			return nil, fmt.Errorf("restore test %d: finished_at: %w", t.ID, err)
		}
		var d TestDetail
		if err := json.Unmarshal(t.Detail, &d); err != nil {
			return nil, fmt.Errorf("restore test %d: detail: %w", t.ID, err)
		}
		st := &status.Test{ID: t.ID, PointID: p.ID, PointTakenAt: tk, Verdict: t.Verdict, FinishedAt: fin, ProvenRTOS: t.ProvenRTOS, Detail: t.Reason,
			SandboxDown: d.SandboxDown}
		if i := slices.IndexFunc(d.Checks, func(c verify.CheckResult) bool { return c.Verdict == verify.Critical }); i >= 0 {
			st.FailedCheck = d.Checks[i].Name
		}
		return st, nil
	}
	if in.LatestTest, err = test(f.LatestTest, f.LatestPoint); err != nil {
		return in, err
	}
	if in.LatestAttempt, err = test(f.LatestAttempt, f.AttemptPoint); err != nil {
		return in, err
	}
	if in.LatestPassingTest, err = test(f.Passing, f.PassingPoint); err != nil {
		return in, err
	}
	return in, nil
}

// failedBackup is the newest backup job when it failed after the newest checked point: why backups stopped, if they
// did (ADR-007 C3). Nil when it succeeded or failed before that point.
func failedBackup(j *store.Job, detail json.RawMessage, newest *status.Point) (*status.Backup, error) {
	if j == nil || j.State != "failed" {
		return nil, nil
	}
	fin, err := time.Parse(time.RFC3339Nano, j.FinishedAt)
	if err != nil {
		return nil, fmt.Errorf("backup job %d: finished_at: %w", j.ID, err)
	}
	if newest != nil && !fin.After(newest.TakenAt) {
		return nil, nil
	}
	var d BackupDetail
	if err := json.Unmarshal(detail, &d); err != nil {
		return nil, fmt.Errorf("backup job %d: detail: %w", j.ID, err)
	}
	return &status.Backup{JobID: j.ID, FinishedAt: fin, Error: j.Error, Unreachable: d.Unreachable, HostKeyUntrusted: d.HostKeyUntrusted}, nil
}

// refreshStatus computes a VM's status now and stores it; a change is recorded with its reason.
func (s *Server) refreshStatus(ctx context.Context, vmID int64) (status.Result, error) {
	r, _, err := s.refreshStatusOf(ctx, vmID)
	return r, err
}

func (s *Server) refreshStatusOf(ctx context.Context, vmID int64) (status.Result, bool, error) {
	in, err := s.statusInput(ctx, vmID)
	if err != nil {
		return status.Result{}, false, err
	}
	r := status.Compute(in)
	changed, err := s.db.SetVMStatus(ctx, vmID, r.Status, r.Reasons[0].Text, r, statusRow(in, r))
	if err != nil {
		return r, false, err
	}
	if changed {
		s.log.Info("VM status changed", "vm", vmID, "status", r.Status, "code", r.Reasons[0].Code, "reason", r.Reasons[0].Text)
	}
	return r, changed, nil
}
