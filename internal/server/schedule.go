package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
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
	return s.refreshStatuses(ctx, now)
}

// queueDueBackups queues a backup for every VM with a policy whose last backup job is at least its interval old.
func (s *Server) queueDueBackups(ctx context.Context, now time.Time) error {
	due, err := s.db.ScheduleFacts(ctx, "backup")
	if err != nil {
		return err
	}
	for _, f := range due {
		if f.Open {
			continue
		}
		if f.LastCreatedAt != "" {
			t, err := time.Parse(time.RFC3339Nano, f.LastCreatedAt)
			if err != nil {
				return err
			}
			if now.Sub(t) < time.Duration(f.BackupIntervalS)*time.Second-tickSlack {
				continue
			}
		}
		id := f.VMID
		if _, err := s.db.CreateJob(ctx, "system", "backup", &id, fmt.Sprintf("schedule: policy %s, every %d s", f.Policy, f.BackupIntervalS)); err != nil {
			return err
		}
	}
	return nil
}

// dispatch starts queued jobs: backups, at most max_parallel_backups at once and one per VM; restore tests as many at
// once as the sandbox has slots (ADR-007 C4), one per VM, in C4's order; migration checks one at a time.
func (s *Server) dispatch(ctx context.Context) error {
	if err := s.dispatchTests(ctx); err != nil {
		return err
	}
	qs, err := s.db.QueuedJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range qs {
		if j.Kind == "migration_check" {
			n := 0
			s.migrating.Range(func(_, _ any) bool { n++; return true })
			if n > 0 {
				continue
			}
			s.migrating.Store(j.ID, true)
			s.jobsWG.Add(1)
			go func(j store.Job) {
				defer s.jobsWG.Done()
				defer s.stateChanged()
				defer s.migrating.Delete(j.ID)
				s.runMigrationCheck(s.jobs, j)
			}(j)
			continue
		}
		if j.Kind == "restore_test" { // dispatchTests
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
			defer s.retryIfStopped(j)
			s.runBackup(s.jobs, j)
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

// Affected is one damaged disk of a point found by a verification. Contents are the damaged contents it uses (ADR-007
// B3): read and found damaged, or found by a search of every point for the contents damaged elsewhere.
type Affected struct {
	PointID  int64    `json:"point_id"`
	VMID     int64    `json:"vm_id"`
	Disk     string   `json:"disk"`
	Problems []string `json:"problems"`
	Contents []string `json:"contents"`
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
		s.verify(s.jobs, actor, r, v)
	}()
	return v, nil
}

// verify reads every present point of the repository that has not failed already (all blocks, or a sample) without the
// cache. When it finds damaged contents it quarantines them (ADR-007 B3): they leave deduplication, every point that
// uses them becomes failed with the content IDs as the reason, every VM of those points must back up full next, and the
// quarantine is an audit entry. A storage error ends the verification as failed and quarantines nothing.
func (s *Server) verify(ctx context.Context, actor string, r store.Repository, v store.Verification) {
	affected := []Affected{}
	skipped := 0
	var q *quarantineResult
	err := func() error {
		lock := s.repoLocks.get(r.ID)
		lock.Lock()
		defer lock.Unlock()
		o, err := s.repoOpts(r)
		if err != nil {
			return err
		}
		st, err := diskstore.OpenUncached(ctx, o)
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
		damaged := []diskstore.Damage{}
		for _, p := range pts {
			if p.CheckState == "failed" { // known to be unusable; pruning removes it
				skipped++
				continue
			}
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
					a := Affected{PointID: p.ID, VMID: p.VMID, Disk: k.Target, Problems: res.Problems, Contents: []string{}}
					for _, d := range res.Damaged {
						if !slices.Contains(a.Contents, d.Content) { // a content used at several offsets of the disk
							a.Contents = append(a.Contents, d.Content)
						}
					}
					affected = append(affected, a)
					damaged = append(damaged, res.Damaged...)
				}
			}
			v.PointsChecked++
		}
		if len(damaged) == 0 {
			return nil
		}
		q, err = s.quarantine(ctx, st, r, v, pts, damaged, &affected)
		return err
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
	vms := map[int64]bool{}
	for _, a := range affected {
		reason := fmt.Sprintf("repository verification %d (%s): disk %s: %s", v.ID, v.Mode, a.Disk, a.Problems[0])
		if len(a.Contents) > 0 {
			reason = fmt.Sprintf("repository verification %d (%s): disk %s uses damaged contents %s (quarantined): %s", v.ID, v.Mode, a.Disk,
				strings.Join(a.Contents, ", "), a.Problems[0])
		}
		if _, err := s.db.SetPointCheck(ctx, actor, a.PointID, false, reason); err != nil {
			s.log.Error("marking a damaged point", "point", a.PointID, "error", err)
		}
		vms[a.VMID] = true
	}
	if q != nil {
		if err := s.recordQuarantine(ctx, actor, r, v, q); err != nil {
			s.log.Error("recording a quarantine", "verification", v.ID, "error", err)
			if v.Error == "" {
				v.Error = "the quarantine was done in the repository but not recorded: " + err.Error()
			}
		}
	}
	for id := range vms {
		if _, err := s.refreshStatus(ctx, id); err != nil {
			s.log.Error("status", "vm", id, "error", err)
		}
	}
	v.PointsSkipped = skipped
	if _, err := s.db.FinishVerification(ctx, actor, v); err != nil {
		s.log.Error("finishing a verification", "verification", v.ID, "error", err)
	}
	s.log.Info("repository verification", "id", v.ID, "repository", r.Name, "mode", v.Mode, "state", v.State, "points", v.PointsChecked,
		"affected", len(affected), "skipped_failed", skipped)
}

// quarantineResult is what a verification quarantined, for the catalog.
type quarantineResult struct {
	Manifest string
	Contents []store.QuarantinedContent
	Full     map[int64]string // VM -> why its next backup must be full
}

// quarantine takes the damaged contents out of deduplication in the repository, and finds every present point that uses
// them, read or not (a sampled verification reads few blocks): those not already among affected are added to it.
func (s *Server) quarantine(ctx context.Context, st *diskstore.Store, r store.Repository, v store.Verification, pts []store.Point,
	damaged []diskstore.Damage, affected *[]Affected) (*quarantineResult, error) {
	seen := map[string]bool{}
	var fresh []diskstore.Damage
	var ids []string
	for _, d := range damaged {
		key := d.Content + "@" + d.Pack
		if seen[key] {
			continue
		}
		seen[key] = true
		ids = append(ids, d.Content)
		done, err := s.db.Quarantined(ctx, r.ID, d.Content, d.Pack)
		if err != nil {
			return nil, err
		}
		if !done {
			fresh = append(fresh, d)
		}
	}
	users, err := st.PointsUsing(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("finding the points that use damaged contents: %w", err)
	}
	type diskOf struct {
		point store.Point
		disk  string
	}
	byStore := map[string]diskOf{}
	for _, p := range pts {
		disks, err := s.db.PointDisks(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		for _, k := range disks {
			byStore[k.StoreID] = diskOf{p, k.Target}
		}
	}
	listed := map[string]bool{}
	for _, a := range *affected {
		listed[fmt.Sprintf("%d/%s", a.PointID, a.Disk)] = true
	}
	pointsOf := map[string][]int64{}
	res := &quarantineResult{Full: map[int64]string{}}
	for sid, cids := range users {
		u, ok := byStore[sid]
		if !ok { // not a present point of the catalog: pruned already, or made by a lost server (T17)
			continue
		}
		for _, c := range cids {
			pointsOf[c] = append(pointsOf[c], u.point.ID)
		}
		if u.point.CheckState != "failed" && !listed[fmt.Sprintf("%d/%s", u.point.ID, u.disk)] {
			listed[fmt.Sprintf("%d/%s", u.point.ID, u.disk)] = true
			*affected = append(*affected, Affected{PointID: u.point.ID, VMID: u.point.VMID, Disk: u.disk, Contents: cids,
				Problems: []string{"uses damaged contents found in other points: " + strings.Join(cids, ", ")}})
		}
	}
	for _, a := range *affected {
		if len(a.Contents) > 0 {
			res.Full[a.VMID] = fmt.Sprintf("full: repository verification %d quarantined damaged contents used by point %d", v.ID, a.PointID)
		}
	}
	if len(fresh) == 0 {
		return res, nil
	}
	qq, err := st.Quarantine(ctx, fresh, fmt.Sprintf("repository verification %d (%s)", v.ID, v.Mode))
	if err != nil {
		return nil, fmt.Errorf("quarantine: %w", err)
	}
	res.Manifest = qq.ID
	for _, d := range fresh {
		pids := slices.Compact(slices.Sorted(slices.Values(pointsOf[d.Content])))
		res.Contents = append(res.Contents, store.QuarantinedContent{ContentID: d.Content, Pack: d.Pack, Reason: d.Reason, Points: pids})
	}
	return res, nil
}

// recordQuarantine writes the quarantine into the catalog (contents, VMs that must back up full) and the audit log.
func (s *Server) recordQuarantine(ctx context.Context, actor string, r store.Repository, v store.Verification, q *quarantineResult) error {
	if err := s.db.RecordQuarantine(ctx, actor, r.ID, v.ID, q.Manifest, q.Contents, q.Full); err != nil {
		return err
	}
	if len(q.Contents) == 0 {
		return nil
	}
	var cs []string
	for _, c := range q.Contents {
		cs = append(cs, fmt.Sprintf("%s (%s; points %v)", c.ContentID, c.Reason, c.Points))
	}
	var vms []string
	for id := range q.Full {
		vms = append(vms, fmt.Sprint(id))
	}
	slices.Sort(vms)
	_, err := s.db.AppendAudit(ctx, store.AuditEntry{Username: actor, Action: "repository quarantine", Target: "repository:" + r.Name,
		Outcome: http.StatusOK, Detail: fmt.Sprintf("verification %d quarantined %d damaged contents (manifest %s): %s; next backup full for VMs %s",
			v.ID, len(q.Contents), q.Manifest, strings.Join(cs, "; "), strings.Join(vms, ", "))})
	return err
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
