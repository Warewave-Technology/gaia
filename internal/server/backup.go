package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/diskstore"
	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/libvirtbackup"
	"github.com/Warewave-Technology/gaia/internal/nbd"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// repoLocks serialise everything that writes a repository or must see it still (backup, retention, maintenance,
// verification) per repository.
type repoLocks struct {
	mu sync.Mutex
	m  map[int64]*sync.Mutex
}

func (l *repoLocks) get(id int64) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[int64]*sync.Mutex{}
	}
	if l.m[id] == nil {
		l.m[id] = &sync.Mutex{}
	}
	return l.m[id]
}

// BackupDetail is a backup job's summary (jobs.detail).
type BackupDetail struct {
	Kind              string                       `json:"kind"`
	Reason            string                       `json:"reason"`
	Checkpoint        string                       `json:"checkpoint"`
	Parent            *int64                       `json:"parent_point,omitempty"`
	Point             int64                        `json:"point,omitempty"`
	Disks             []DiskDetail                 `json:"disks"`
	StaleJobEnded     bool                         `json:"stale_job_ended,omitempty"`
	Check             []string                     `json:"check_problems"`
	CheckpointsKept   []string                     `json:"checkpoints_kept"`
	CheckpointsPruned []string                     `json:"checkpoints_deleted"`
	PointsPruned      []int64                      `json:"points_pruned"`
	Maintenance       *diskstore.MaintenanceResult `json:"maintenance,omitempty"`
	Timings           map[string]float64           `json:"timings_s"`
	Libvirt           string                       `json:"libvirt"`
	QEMU              string                       `json:"qemu"`
}

// DiskDetail is what a backup did for one disk.
type DiskDetail struct {
	Target        string `json:"target"`
	SizeBytes     uint64 `json:"size_bytes"`
	MetaContext   string `json:"meta_context"`
	ChangedBytes  uint64 `json:"changed_bytes"`
	ReadBytes     uint64 `json:"read_bytes"`        // asked for over NBD
	PayloadBytes  uint64 `json:"nbd_payload_bytes"` // came back as data
	RepoReadBytes uint64 `json:"repo_read_bytes"`   // parent blocks read back to complete partly changed blocks
	WrittenBytes  uint64 `json:"written_bytes"`
	StoreID       string `json:"store_id"`
}

// hostTarget is how to reach a host.
func hostTarget(h store.Host) hostconn.Target {
	return hostconn.Target{Address: h.Address, Port: h.SSHPort, User: h.SSHUser, HostKey: h.SSHHostKey}
}

// fullReason decides whether the next backup of a VM must be full, and why. demand is the VM's recorded demand for a
// full backup (a quarantine, ADR-007 B3; "" = none); parent is the VM's latest checked point (nil if none); host lists
// the checkpoints on the host; disks are the VM's disks now.
func fullReason(demand string, parent *store.Point, parentDisks []store.PointDisk, host []string, disks []libvirtbackup.Disk, repoID int64) string {
	switch {
	case demand != "":
		return demand
	case parent == nil:
		return "full: no earlier checked point of this VM"
	case parent.RepositoryID != repoID:
		return fmt.Sprintf("full: the policy's repository changed (point %d is in repository %d)", parent.ID, parent.RepositoryID)
	case parent.Checkpoint == "" || !slices.Contains(host, parent.Checkpoint):
		return fmt.Sprintf("full: checkpoint %s of point %d is not on the host", parent.Checkpoint, parent.ID)
	}
	had := map[string]int64{}
	for _, k := range parentDisks {
		had[k.Target] = k.SizeBytes
	}
	var reasons []string
	for _, d := range disks {
		size, ok := had[d.Target]
		switch {
		case !ok:
			reasons = append(reasons, "disk "+d.Target+" is new")
		case uint64(size) != d.Capacity:
			reasons = append(reasons, fmt.Sprintf("disk %s was resized from %d to %d bytes", d.Target, size, d.Capacity))
		}
		delete(had, d.Target)
	}
	for t := range had {
		reasons = append(reasons, "disk "+t+" was removed")
	}
	if len(reasons) > 0 {
		slices.Sort(reasons)
		return "full: " + strings.Join(reasons, "; ")
	}
	return ""
}

// runBackup does one queued backup job; the job's state says how it ended.
func (s *Server) runBackup(ctx context.Context, j store.Job) {
	d := &BackupDetail{Timings: map[string]float64{}, Disks: []DiskDetail{}, Check: []string{}, CheckpointsKept: []string{},
		CheckpointsPruned: []string{}, PointsPruned: []int64{}}
	start := time.Now()
	err := s.backup(ctx, j, d)
	d.Timings["total"] = time.Since(start).Seconds()
	if derr := s.db.SetJobDetail(context.WithoutCancel(ctx), j.ID, d); derr != nil {
		s.log.Error("job detail", "job", j.ID, "error", derr)
	}
	to, msg := "ok", ""
	if err != nil {
		to, msg = "failed", err.Error()
		s.log.Error("backup failed", "job", j.ID, "error", err)
	} else {
		s.log.Info("backup done", "job", j.ID, "point", d.Point, "kind", d.Kind, "seconds", d.Timings["total"])
	}
	if _, err := s.db.SetJobState(context.WithoutCancel(ctx), "system", j.ID, to, d.Reason, msg); err != nil {
		s.log.Error("job state", "job", j.ID, "error", err)
	}
	if j.VMID != nil {
		if _, err := s.refreshStatus(context.WithoutCancel(ctx), *j.VMID); err != nil {
			s.log.Error("status", "vm", *j.VMID, "error", err)
		}
	}
}

func (s *Server) backup(ctx context.Context, j store.Job, d *BackupDetail) error {
	if j.VMID == nil {
		return errors.New("a backup job without a VM")
	}
	vm, err := s.db.VM(ctx, *j.VMID)
	if err != nil {
		return err
	}
	if vm.PolicyID == nil {
		return fmt.Errorf("VM %s has no policy", vm.Name)
	}
	pol, err := s.db.Policy(ctx, *vm.PolicyID)
	if err != nil {
		return err
	}
	repo, err := s.db.Repository(ctx, pol.RepositoryID)
	if err != nil {
		return err
	}
	host, err := s.db.Host(ctx, vm.HostID)
	if err != nil {
		return err
	}
	if _, err := s.db.SetJobState(ctx, "system", j.ID, "running", "worker started", ""); err != nil {
		return err
	}
	t := time.Now()
	hc, err := hostconn.Dial(ctx, hostTarget(host), s.sshKey)
	if err != nil {
		return err
	}
	defer hc.Close()
	lv, err := hc.Libvirt()
	if err != nil {
		return err
	}
	defer lv.Close()
	if d.Libvirt, d.QEMU, err = lv.Versions(); err != nil {
		return err
	}
	d.Timings["connect"] = time.Since(t).Seconds()
	// The NBD socket goes into the VM's own directory on the host, the one place its AppArmor profile lets QEMU
	// create it; gaia's host user reaches it through the group libvirt-qemu.
	domDir, err := lv.DomainDir(vm.Name, s.cfg.HostSocketDir)
	if err != nil {
		return err
	}
	socketPrefix := domDir + "/gaia-"
	if d.StaleJobEnded, err = lv.AbortStaleJob(vm.Name, socketPrefix); err != nil {
		return err
	}
	disks, uuid, err := lv.Disks(vm.Name)
	if err != nil {
		return err
	}
	machine, err := lv.Machine(vm.Name)
	if err != nil {
		return err
	}
	machineJSON, err := json.Marshal(machine)
	if err != nil {
		return err
	}
	if len(disks) == 0 {
		return fmt.Errorf("VM %s has no file-backed disk to back up", vm.Name)
	}
	var now []store.DiskInput
	var targets []string
	for _, k := range disks {
		now = append(now, store.DiskInput{Target: k.Target, SizeBytes: int64(k.Capacity)})
		targets = append(targets, k.Target)
	}
	if err := s.db.SyncDisks(ctx, "system", vm.ID, now); err != nil {
		return err
	}
	if vm.UUID == "" && uuid != "" {
		if _, err := s.db.UpdateVM(ctx, vm.ID, store.VMPatch{UUID: &uuid}); err != nil {
			return err
		}
	}
	cps, err := lv.Checkpoints(vm.Name)
	if err != nil {
		return err
	}

	// The repository is locked from here on: a verification cannot quarantine contents (and demand a full backup) between
	// the decision below and the backup it decides.
	lock := s.repoLocks.get(repo.ID)
	lock.Lock()
	defer lock.Unlock()
	st, err := s.OpenRepository(ctx, repo)
	if err != nil {
		return err
	}
	defer st.Close(ctx)
	if vm, err = s.db.VM(ctx, vm.ID); err != nil {
		return err
	}

	// Full or incremental, and why.
	var parent *store.Point
	var parentDisks []store.PointDisk
	if p, err := s.db.LatestPoint(ctx, vm.ID); err == nil {
		parent = &p
		if parentDisks, err = s.db.PointDisks(ctx, p.ID); err != nil {
			return err
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	d.Kind, d.Reason = "full", fullReason(vm.FullBackupReason, parent, parentDisks, cps, disks, repo.ID)
	if d.Reason == "" {
		d.Kind, d.Parent = "incremental", &parent.ID
		d.Reason = fmt.Sprintf("incremental from point %d (checkpoint %s)", parent.ID, parent.Checkpoint)
	}

	d.Checkpoint = fmt.Sprintf("gaia-%d-%d", vm.ID, j.ID)
	if s.restoreTag != "" {
		d.Checkpoint += "-" + s.restoreTag
	}
	req := libvirtbackup.MultiRequest{Domain: vm.Name, Disks: targets, Checkpoint: d.Checkpoint,
		Server:     libvirtbackup.Server{Network: "unix", Addr: fmt.Sprintf("%s%d.sock", socketPrefix, j.ID)},
		ScratchDir: s.cfg.HostScratchDir}
	if d.Kind == "incremental" {
		req.Incremental = parent.Checkpoint
	}
	t = time.Now()
	job, err := lv.BeginMulti(req)
	if err != nil {
		return err
	}
	takenAt := time.Now().UTC()
	d.Timings["begin"] = time.Since(t).Seconds()
	backupErr := func() error {
		if err := s.db.SetJobCheckpoint(ctx, "system", j.ID, d.Checkpoint); err != nil {
			return err
		}
		for _, e := range job.Exports {
			var size uint64
			for _, k := range disks {
				if k.Target == e.Disk {
					size = k.Capacity
				}
			}
			var pd *diskstore.Point
			if d.Kind == "incremental" {
				for _, k := range parentDisks {
					if k.Target == e.Disk {
						if pd, err = st.Point(ctx, k.StoreID); err != nil {
							return fmt.Errorf("parent point %d disk %s: %w", parent.ID, k.Target, err)
						}
					}
				}
			}
			t := time.Now()
			dd, err := s.backupDisk(ctx, hc, st, job, e, vm.Name, d.Checkpoint, size, pd)
			if err != nil {
				return fmt.Errorf("disk %s: %w", e.Disk, err)
			}
			d.Timings["disk_"+e.Disk] = time.Since(t).Seconds()
			d.Disks = append(d.Disks, dd)
		}
		return nil
	}()
	endErr := job.End()
	if backupErr != nil || endErr != nil {
		// No point refers to this checkpoint: remove it, so it is neither kept nor taken as a parent.
		if derr := lv.DeleteCheckpoint(vm.Name, d.Checkpoint); derr != nil {
			s.log.Warn("removing the checkpoint of a failed backup", "checkpoint", d.Checkpoint, "error", derr)
		} else {
			d.CheckpointsPruned = append(d.CheckpointsPruned, d.Checkpoint)
		}
		return errors.Join(backupErr, endErr)
	}

	// The point, and its post-backup check (core matrix #1).
	var pdisks []store.PointDisk
	for _, dd := range d.Disks {
		pdisks = append(pdisks, store.PointDisk{Target: dd.Target, SizeBytes: int64(dd.SizeBytes), BlockSize: diskstore.BlockSize, StoreID: dd.StoreID,
			ChangedBytes: int64(dd.ChangedBytes), ReadBytes: int64(dd.ReadBytes), WrittenBytes: int64(dd.WrittenBytes)})
		if sp, err := st.Point(ctx, dd.StoreID); err == nil {
			pdisks[len(pdisks)-1].BlockSize = int64(sp.BlockSize)
		}
	}
	pt, err := s.db.CreateBackupPoint(ctx, "system", store.Point{VMID: vm.ID, JobID: j.ID, RepositoryID: repo.ID, Kind: d.Kind,
		TakenAt: takenAt.Format(time.RFC3339Nano), Checkpoint: d.Checkpoint, ParentID: d.Parent, Reason: d.Reason, Machine: machineJSON}, pdisks)
	if err != nil {
		return err
	}
	d.Point = pt.ID
	t = time.Now()
	for _, dd := range d.Disks {
		c, err := st.CheckPoint(ctx, dd.StoreID)
		if err != nil {
			return err
		}
		for _, p := range c.Problems {
			d.Check = append(d.Check, dd.Target+": "+p)
		}
	}
	d.Timings["check"] = time.Since(t).Seconds()
	reason := "post-backup check: manifest and block index read back, every block present, sizes match"
	if len(d.Check) > 0 {
		reason = "post-backup check failed: " + strings.Join(d.Check, "; ")
	}
	if _, err := s.db.SetPointCheck(ctx, "system", pt.ID, len(d.Check) == 0, reason); err != nil {
		return err
	}
	if len(d.Check) > 0 {
		return errors.New(reason)
	}
	if d.Kind == "full" && vm.FullBackupReason != "" {
		if err := s.db.ClearFullBackupReason(ctx, "system", vm.ID, fmt.Sprintf("full backup point %d (job %d) passed its post-backup check", pt.ID, j.ID)); err != nil {
			return err
		}
	}

	// Checkpoints on the host: the new one and its parent's stay; older ones of this server go.
	keep := []string{d.Checkpoint}
	if parent != nil {
		keep = append(keep, parent.Checkpoint)
	}
	if cps, err = lv.Checkpoints(vm.Name); err != nil {
		return err
	}
	for _, cp := range cps {
		mine := strings.HasPrefix(cp, fmt.Sprintf("gaia-%d-", vm.ID))
		if !mine {
			continue
		}
		if slices.Contains(keep, cp) {
			d.CheckpointsKept = append(d.CheckpointsKept, cp)
			continue
		}
		if err := lv.DeleteCheckpoint(vm.Name, cp); err != nil {
			return fmt.Errorf("checkpoint retention: %w", err)
		}
		d.CheckpointsPruned = append(d.CheckpointsPruned, cp)
	}

	// Retention, then maintenance as it is due.
	t = time.Now()
	pruned, err := s.applyRetention(ctx, st, vm, pol)
	if err != nil {
		return fmt.Errorf("retention: %w", err)
	}
	d.PointsPruned = pruned
	m, err := st.Maintain(ctx, false, s.maintenanceSafety)
	if err != nil {
		return fmt.Errorf("maintenance: %w", err)
	}
	if m.Mode != "" {
		d.Maintenance = &m
	}
	d.Timings["retention_maintenance"] = time.Since(t).Seconds()
	return nil
}

// backupDisk reads one exported disk over NBD connections forwarded through SSH and writes its diskstore point.
func (s *Server) backupDisk(ctx context.Context, hc *hostconn.Conn, st *diskstore.Store, job *libvirtbackup.MultiJob, e libvirtbackup.Export,
	domain, checkpoint string, size uint64, parent *diskstore.Point) (DiskDetail, error) {
	dd := DiskDetail{Target: e.Disk, SizeBytes: size, MetaContext: nbd.ContextBaseAllocation}
	if e.Bitmap != "" {
		dd.MetaContext = nbd.DirtyBitmapPrefix + e.Bitmap
	}
	dial := func(contexts []string) (*nbd.Client, error) {
		conn, err := hc.DialUnix(job.Server.Addr)
		if err != nil {
			return nil, err
		}
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		c, err := nbd.NewClient(dctx, conn, nbd.Options{Export: e.Export, MetaContexts: contexts})
		if err != nil {
			conn.Close()
			return nil, err
		}
		return c, nil
	}
	first, err := dial([]string{dd.MetaContext})
	if err != nil {
		return dd, fmt.Errorf("NBD export %q: %w", e.Export, err)
	}
	clients := []*nbd.Client{first}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	for len(clients) < s.cfg.NBDConnections {
		c, err := dial(nil)
		if err != nil {
			return dd, fmt.Errorf("NBD connection %d: %w", len(clients)+1, err)
		}
		clients = append(clients, c)
	}
	if first.Size() != size {
		return dd, fmt.Errorf("the export is %d bytes, libvirt says the disk is %d", first.Size(), size)
	}
	ext, err := first.Extents(dd.MetaContext, 0, size)
	if err != nil {
		return dd, err
	}
	var changed []diskstore.Range
	for _, x := range ext {
		need := x.Flags&(nbd.StateHole|nbd.StateZero) == 0
		if e.Bitmap != "" {
			need = x.Flags&nbd.StateDirty != 0
		}
		if need {
			changed = append(changed, diskstore.Range{Offset: x.Offset, Length: x.Length})
			dd.ChangedBytes += x.Length
		}
	}
	sources := make([]diskstore.Source, len(clients))
	for i, c := range clients {
		sources[i] = c
	}
	pt, err := st.Backup(ctx, diskstore.BackupRequest{Domain: domain, Disk: e.Disk, Checkpoint: checkpoint, Size: size, Changed: changed,
		Parent: parent, Sources: sources})
	if err != nil {
		return dd, err
	}
	dd.StoreID = pt.ID
	dd.ReadBytes, dd.RepoReadBytes = pt.Stats.SourceReadBytes, pt.Stats.RepoReadBytes
	dd.WrittenBytes = uint64(pt.Stats.BlocksWritten) * pt.BlockSize
	for _, c := range clients {
		b, _ := c.Stats()
		dd.PayloadBytes += b
	}
	return dd, nil
}

// keepPoints applies a policy's retention to points (oldest first): the newest keep_last, the newest of each of the
// last keep_daily days and keep_weekly ISO weeks (UTC). The newest point is always kept.
func keepPoints(points []store.Point, pol store.Policy) map[int64]bool {
	keep := map[int64]bool{}
	newest := slices.Clone(points)
	slices.Reverse(newest)
	days, weeks := map[string]bool{}, map[string]bool{}
	for i, p := range newest {
		if i < pol.KeepLast {
			keep[p.ID] = true
		}
		t, err := time.Parse(time.RFC3339Nano, p.TakenAt)
		if err != nil {
			keep[p.ID] = true // a time it cannot read is not a reason to delete
			continue
		}
		day := t.UTC().Format("2006-01-02")
		if !days[day] && len(days) < pol.KeepDaily {
			days[day], keep[p.ID] = true, true
		}
		y, w := t.UTC().ISOWeek()
		week := fmt.Sprintf("%d-W%02d", y, w)
		if !weeks[week] && len(weeks) < pol.KeepWeekly {
			weeks[week], keep[p.ID] = true, true
		}
	}
	if len(newest) > 0 {
		keep[newest[0].ID] = true
	}
	return keep
}

// PointForget is the body of POST /points/{id}/forget.
type PointForget struct {
	Reason string `json:"reason"` // why the point may go, for the audit log and the point's state change; required
}

// ForgetPoint deletes a failed point from its repository and marks it pruned, as retention would (ADR-007 B4.3: a
// failed point keeps its VM CRITICAL until pruning removes it). Only a failed point: a damaged point cannot be
// restored from anyway, and a person decides when it may go, with the reason; retention handles every other point.
func (s *Server) ForgetPoint(ctx context.Context, actor string, id int64, reason string) (store.Point, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return store.Point{}, fmt.Errorf("%w: reason: say why the point may go (it is recorded)", store.ErrInvalid)
	}
	p, err := s.db.Point(ctx, id)
	if err != nil {
		return p, err
	}
	switch {
	case p.State != "present":
		return p, fmt.Errorf("%w: point %d is %s", store.ErrConflict, id, p.State)
	case p.CheckState != "failed":
		return p, fmt.Errorf("%w: point %d is not failed (its check is %s): only a damaged point can be forgotten; retention removes the others", store.ErrConflict, id, p.CheckState)
	}
	r, err := s.db.Repository(ctx, p.RepositoryID)
	if err != nil {
		return p, err
	}
	disks, err := s.db.PointDisks(ctx, p.ID)
	if err != nil {
		return p, err
	}
	err = func() error {
		lock := s.repoLocks.get(r.ID)
		lock.Lock()
		defer lock.Unlock()
		st, err := s.OpenRepository(ctx, r)
		if err != nil {
			return err
		}
		defer st.Close(ctx)
		for _, k := range disks {
			if err := st.DeletePoint(ctx, k.StoreID); err != nil {
				return fmt.Errorf("point %d disk %s: %w", p.ID, k.Target, err)
			}
		}
		return nil
	}()
	if err != nil {
		return p, err
	}
	if err := s.db.PrunePoint(ctx, actor, p.ID, "forgotten by "+actor+": "+reason); err != nil {
		return p, err
	}
	if _, err := s.refreshStatus(ctx, p.VMID); err != nil {
		return p, err
	}
	s.stateChanged()
	return s.db.Point(ctx, id)
}

// applyRetention prunes the VM's points the policy does not keep: their diskstore points are deleted (their blocks go
// at the next garbage collection) and their rows marked pruned.
func (s *Server) applyRetention(ctx context.Context, st *diskstore.Store, vm store.VM, pol store.Policy) ([]int64, error) {
	pts, err := s.db.PresentPoints(ctx, vm.ID, 0)
	if err != nil {
		return nil, err
	}
	keep := keepPoints(pts, pol)
	pruned := []int64{}
	for _, p := range pts {
		if keep[p.ID] {
			continue
		}
		disks, err := s.db.PointDisks(ctx, p.ID)
		if err != nil {
			return pruned, err
		}
		for _, k := range disks {
			if err := st.DeletePoint(ctx, k.StoreID); err != nil {
				return pruned, fmt.Errorf("point %d disk %s: %w", p.ID, k.Target, err)
			}
		}
		if err := s.db.PrunePoint(ctx, "system", p.ID, fmt.Sprintf("retention: keep_last %d, keep_daily %d, keep_weekly %d", pol.KeepLast, pol.KeepDaily, pol.KeepWeekly)); err != nil {
			return pruned, err
		}
		pruned = append(pruned, p.ID)
	}
	return pruned, nil
}
