package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// tick is how often the scheduler looks; tickSlack lets a backup that falls due a few milliseconds after a tick
// start at that tick (a job's created_at is written just after its tick, so the next one would otherwise slip a whole
// tick every cycle).
const (
	tick      = 15 * time.Second
	tickSlack = time.Second
)

// schedule queues backups as policies say, starts queued backups, and starts the daily sampled and weekly full
// repository verifications, until ctx ends.
func (s *Server) schedule(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := s.tick(ctx, time.Now()); err != nil && ctx.Err() == nil {
			s.log.Error("scheduler", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) tick(ctx context.Context, now time.Time) error {
	if err := s.queueDueBackups(ctx, now); err != nil {
		return err
	}
	if err := s.queueDueTests(ctx, now); err != nil {
		return err
	}
	if err := s.dispatch(ctx); err != nil {
		return err
	}
	if err := s.startDueVerifications(ctx, now); err != nil {
		return err
	}
	return s.refreshAllStatuses(ctx)
}

// queueDueBackups queues a backup for every VM with a policy whose last backup job is at least its interval old.
func (s *Server) queueDueBackups(ctx context.Context, now time.Time) error {
	vms, err := s.db.VMs(ctx, 0)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if vm.PolicyID == nil {
			continue
		}
		if _, open, err := s.db.OpenJob(ctx, vm.ID, "backup"); err != nil || open {
			if err != nil {
				return err
			}
			continue
		}
		pol, err := s.db.Policy(ctx, *vm.PolicyID)
		if err != nil {
			return err
		}
		last, ok, err := s.db.LastJob(ctx, vm.ID, "backup")
		if err != nil {
			return err
		}
		if ok {
			t, err := time.Parse(time.RFC3339Nano, last.CreatedAt)
			if err != nil {
				return err
			}
			if now.Sub(t) < time.Duration(pol.BackupIntervalS)*time.Second-tickSlack {
				continue
			}
		}
		id := vm.ID
		if _, err := s.db.CreateJob(ctx, "system", "backup", &id, fmt.Sprintf("schedule: policy %s, every %d s", pol.Name, pol.BackupIntervalS)); err != nil {
			return err
		}
	}
	return nil
}

// dispatch starts queued jobs: backups, at most max_parallel_backups at once and one per VM; restore tests one at a
// time (the sandbox boots one guest at a time).
func (s *Server) dispatch(ctx context.Context) error {
	qs, err := s.db.QueuedJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range qs {
		if j.Kind == "restore_test" && j.VMID != nil {
			n := 0
			s.testing.Range(func(_, _ any) bool { n++; return true })
			if n > 0 {
				continue
			}
			s.testing.Store(*j.VMID, j.ID)
			s.jobsWG.Add(1)
			go func(j store.Job) {
				defer s.jobsWG.Done()
				defer s.stateChanged()
				defer s.testing.Delete(*j.VMID)
				s.runTest(ctx, j)
			}(j)
			continue
		}
		if j.Kind != "backup" || j.VMID == nil {
			continue
		}
		n := 0
		s.running.Range(func(_, _ any) bool { n++; return true })
		if n >= s.cfg.MaxParallelBackups {
			return nil
		}
		if _, busy := s.running.LoadOrStore(*j.VMID, j.ID); busy {
			continue
		}
		s.jobsWG.Add(1)
		go func(j store.Job) {
			defer s.jobsWG.Done()
			defer s.stateChanged()
			defer s.running.Delete(*j.VMID)
			s.runBackup(ctx, j)
		}(j)
	}
	return nil
}

// startDueVerifications starts, per repository, the full verification on full_verify_day and the sampled one on the
// other days, each once a day from verify_at on.
func (s *Server) startDueVerifications(ctx context.Context, now time.Time) error {
	at := nextRun(now.Add(-24*time.Hour), s.cfg.VerifyAt) // today's (or, before verify_at, yesterday's) slot
	if now.Before(at) {
		return nil
	}
	mode := "sampled"
	if now.Weekday() == weekday(s.cfg.FullVerifyDay) {
		mode = "full"
	}
	repos, err := s.db.Repositories(ctx)
	if err != nil {
		return err
	}
	for _, r := range repos {
		vs, err := s.db.Verifications(ctx, r.ID, 1)
		if err != nil {
			return err
		}
		if len(vs) > 0 {
			t, err := time.Parse(time.RFC3339Nano, vs[0].StartedAt)
			if err != nil {
				return err
			}
			if !t.Before(at) || vs[0].State == "running" {
				continue
			}
		}
		if _, err := s.StartVerification(ctx, "system", r, mode, "schedule"); err != nil {
			return err
		}
	}
	return nil
}

// Affected is one damaged disk of a point found by a verification.
type Affected struct {
	PointID  int64    `json:"point_id"`
	VMID     int64    `json:"vm_id"`
	Disk     string   `json:"disk"`
	Problems []string `json:"problems"`
}

// StartVerification records a verification of a repository and runs it in the background.
func (s *Server) StartVerification(ctx context.Context, actor string, r store.Repository, mode, reason string) (store.Verification, error) {
	if mode != "sampled" && mode != "full" {
		return store.Verification{}, fmt.Errorf("%w: mode %q (sampled or full)", store.ErrInvalid, mode)
	}
	v, err := s.db.StartVerification(ctx, actor, r.ID, mode, reason)
	if err != nil {
		return v, err
	}
	s.jobsWG.Add(1)
	go func() {
		defer s.jobsWG.Done()
		defer s.stateChanged()
		s.verify(ctx, actor, r, v)
	}()
	return v, nil
}

// verify reads every present point of the repository (all blocks, or a sample) without the cache, and names the
// points it finds damaged; their check state becomes failed.
func (s *Server) verify(ctx context.Context, actor string, r store.Repository, v store.Verification) {
	affected := []Affected{}
	err := func() error {
		lock := s.repoLocks.get(r.ID)
		lock.Lock()
		defer lock.Unlock()
		pw, err := os.ReadFile(s.repoPasswordFile(r.Name))
		if err != nil {
			return err
		}
		st, err := diskstore.OpenUncached(ctx, s.repoOptions(r.Name, r.Location, strings.TrimSpace(string(pw))))
		if err != nil {
			return err
		}
		defer st.Close(ctx)
		pts, err := s.db.PresentPoints(ctx, 0, r.ID)
		if err != nil {
			return err
		}
		fraction := 1.0
		if v.Mode == "sampled" {
			fraction = s.cfg.VerifySample
		}
		rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(v.ID)))
		for _, p := range pts {
			disks, err := s.db.PointDisks(ctx, p.ID)
			if err != nil {
				return err
			}
			for _, k := range disks {
				res, err := st.VerifyPoint(ctx, k.StoreID, fraction, rng)
				if err != nil {
					return err
				}
				v.BlocksRead += int64(res.Read)
				v.BytesRead += res.BytesRead
				if len(res.Problems) > 0 {
					affected = append(affected, Affected{PointID: p.ID, VMID: p.VMID, Disk: k.Target, Problems: res.Problems})
				}
			}
			v.PointsChecked++
		}
		return nil
	}()
	v.State = "ok"
	switch {
	case err != nil:
		v.State, v.Error = "failed", err.Error()
	case len(affected) > 0:
		v.State = "critical"
	}
	b, _ := json.Marshal(affected)
	v.Affected = b
	ctx = context.WithoutCancel(ctx)
	for _, a := range affected {
		if _, err := s.db.SetPointCheck(ctx, actor, a.PointID, false, fmt.Sprintf("repository verification %d (%s): disk %s: %s", v.ID, v.Mode, a.Disk, a.Problems[0])); err != nil {
			s.log.Error("marking a damaged point", "point", a.PointID, "error", err)
		}
		if _, err := s.refreshStatus(ctx, a.VMID); err != nil {
			s.log.Error("status", "vm", a.VMID, "error", err)
		}
	}
	if _, err := s.db.FinishVerification(ctx, actor, v); err != nil {
		s.log.Error("finishing a verification", "verification", v.ID, "error", err)
	}
	s.log.Info("repository verification", "id", v.ID, "repository", r.Name, "mode", v.Mode, "state", v.State, "points", v.PointsChecked, "affected", len(affected))
}

// RestoredDisk is one disk of a point restored and hashed.
type RestoredDisk struct {
	Target  string  `json:"target"`
	Bytes   int64   `json:"bytes"`
	SHA256  string  `json:"sha256"`
	Seconds float64 `json:"seconds"`
}

// restoreCheck restores every disk of a point to a scratch file, hashes it and removes it: the point restores, and the
// hashes can be compared with a reference.
func (s *Server) restoreCheck(ctx context.Context, p store.Point) ([]RestoredDisk, error) {
	r, err := s.db.Repository(ctx, p.RepositoryID)
	if err != nil {
		return nil, err
	}
	disks, err := s.db.PointDisks(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	lock := s.repoLocks.get(r.ID)
	lock.Lock()
	defer lock.Unlock()
	st, err := s.OpenRepository(ctx, r)
	if err != nil {
		return nil, err
	}
	defer st.Close(ctx)
	if err := os.MkdirAll(s.cfg.tmpDir(), 0o700); err != nil {
		return nil, err
	}
	out := []RestoredDisk{}
	for _, k := range disks {
		sp, err := st.Point(ctx, k.StoreID)
		if err != nil {
			return out, err
		}
		f, err := os.CreateTemp(s.cfg.tmpDir(), "restore-check-*.raw")
		if err != nil {
			return out, err
		}
		path := f.Name()
		f.Close()
		os.Remove(path) // Restore creates it
		t := time.Now()
		_, rerr := st.Restore(ctx, sp, path, 4)
		var sum string
		var n int64
		if rerr == nil {
			sum, n, rerr = hashFile(path)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			s.log.Error("removing a restore-check file", "file", path, "error", err)
		}
		if rerr != nil {
			return out, fmt.Errorf("disk %s: %w", k.Target, rerr)
		}
		out = append(out, RestoredDisk{Target: k.Target, Bytes: n, SHA256: sum, Seconds: time.Since(t).Seconds()})
	}
	return out, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 4<<20))
	return hex.EncodeToString(h.Sum(nil)), n, err
}
