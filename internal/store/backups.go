package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PointDisk is one disk of a point.
type PointDisk struct {
	ID           int64  `json:"id"`
	PointID      int64  `json:"point_id"`
	Target       string `json:"target"`
	SizeBytes    int64  `json:"size_bytes"`
	BlockSize    int64  `json:"block_size"`
	StoreID      string `json:"store_id"`
	ChangedBytes int64  `json:"changed_bytes"`
	ReadBytes    int64  `json:"read_bytes"`
	WrittenBytes int64  `json:"written_bytes"`
}

// CreateBackupPoint records a point and its disks in one transaction; its post-backup check starts unchecked.
func (d *DB) CreateBackupPoint(ctx context.Context, actor string, p Point, disks []PointDisk) (Point, error) {
	if len(disks) == 0 {
		return Point{}, fmt.Errorf("%w: a point has at least one disk", ErrInvalid)
	}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var total int64
		for _, k := range disks {
			total += k.WrittenBytes
		}
		machine := string(p.Machine)
		if machine == "" {
			machine = "{}"
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO points (vm_id, job_id, repository_id, kind, taken_at, check_state, bytes, state, checkpoint, parent_id, reason, machine)
			VALUES (?, ?, ?, ?, ?, 'unchecked', ?, 'present', ?, ?, ?, ?)`, p.VMID, p.JobID, p.RepositoryID, p.Kind, p.TakenAt, total, p.Checkpoint, p.ParentID, p.Reason, machine)
		if err != nil {
			return err
		}
		if p.ID, err = r.LastInsertId(); err != nil {
			return err
		}
		for _, k := range disks {
			if _, err := tx.ExecContext(ctx, `INSERT INTO point_disks (point_id, target, size_bytes, block_size, store_id, changed_bytes, read_bytes, written_bytes)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, p.ID, k.Target, k.SizeBytes, k.BlockSize, k.StoreID, k.ChangedBytes, k.ReadBytes, k.WrittenBytes); err != nil {
				return err
			}
		}
		return d.recordState(ctx, tx, "point", p.ID, "", "unchecked", fmt.Sprintf("%s point of job %d: %s", p.Kind, p.JobID, p.Reason), actor)
	})
	if err != nil {
		return Point{}, err
	}
	return d.Point(ctx, p.ID)
}

// PointDisks lists a point's disks.
func (d *DB) PointDisks(ctx context.Context, pointID int64) ([]PointDisk, error) {
	return rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (PointDisk, error) {
		var k PointDisk
		err := s.Scan(&k.ID, &k.PointID, &k.Target, &k.SizeBytes, &k.BlockSize, &k.StoreID, &k.ChangedBytes, &k.ReadBytes, &k.WrittenBytes)
		return k, err
	}, `SELECT id, point_id, target, size_bytes, block_size, store_id, changed_bytes, read_bytes, written_bytes FROM point_disks
		WHERE point_id = ? ORDER BY target`, pointID)
}

// LatestPoint is a VM's newest present point that passed its post-backup check: the parent of the next incremental.
func (d *DB) LatestPoint(ctx context.Context, vmID int64) (Point, error) {
	p, err := scanPoint(d.db.QueryRowContext(ctx, `SELECT `+pointCols+` FROM points WHERE vm_id = ? AND state = 'present' AND check_state = 'ok'
		ORDER BY id DESC LIMIT 1`, vmID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("%w: no checked point of VM %d", ErrNotFound, vmID)
	}
	return p, err
}

// PresentPoints lists the present points of a VM (vmID > 0) or of a repository (repoID > 0), oldest first.
func (d *DB) PresentPoints(ctx context.Context, vmID, repoID int64) ([]Point, error) {
	q, arg := `SELECT `+pointCols+` FROM points WHERE state = 'present' AND vm_id = ? ORDER BY id`, vmID
	if repoID > 0 {
		q, arg = `SELECT `+pointCols+` FROM points WHERE state = 'present' AND repository_id = ? ORDER BY id`, repoID
	}
	return rowsOf(ctx, d.db, scanPoint, q, arg)
}

// SetPointCheck records the outcome of a check of a point (post-backup check or repository verification).
func (d *DB) SetPointCheck(ctx context.Context, actor string, id int64, ok bool, reason string) (Point, error) {
	to := "ok"
	if !ok {
		to = "failed"
	}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		p, err := scanPoint(tx.QueryRowContext(ctx, `SELECT `+pointCols+` FROM points WHERE id = ?`, id))
		if err != nil {
			return notFound("point", id, err)
		}
		if p.CheckState == to {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE points SET check_state = ? WHERE id = ?`, to, id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "point_check", id, p.CheckState, to, reason, actor)
	})
	if err != nil {
		return Point{}, err
	}
	return d.Point(ctx, id)
}

// PrunePoint marks a present point pruned (its data was deleted by retention).
func (d *DB) PrunePoint(ctx context.Context, actor string, id int64, reason string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE points SET state = 'pruned' WHERE id = ? AND state = 'present'`, id)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("%w: point %d is not present", ErrConflict, id)
		}
		return d.recordState(ctx, tx, "point", id, "present", "pruned", reason, actor)
	})
}

// SetJobCheckpoint records the checkpoint the host created for a running job.
func (d *DB) SetJobCheckpoint(ctx context.Context, actor string, id int64, checkpoint string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET checkpoint = ? WHERE id = ?`, checkpoint, id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "job", id, "running", "running", "checkpoint "+checkpoint+" created on the host", actor)
	})
}

// SetJobDetail stores a job's JSON summary.
func (d *DB) SetJobDetail(ctx context.Context, id int64, detail any) error {
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `UPDATE jobs SET detail = ? WHERE id = ?`, string(b), id)
	return err
}

// JobInfo is a job with its checkpoint and detail (schema 3).
type JobInfo struct {
	Job
	Checkpoint string          `json:"checkpoint"`
	Detail     json.RawMessage `json:"detail"`
}

// JobInfo returns a job with its checkpoint and detail.
func (d *DB) JobInfo(ctx context.Context, id int64) (JobInfo, error) {
	j, err := d.Job(ctx, id)
	if err != nil {
		return JobInfo{}, err
	}
	ji := JobInfo{Job: j}
	var detail string
	if err := d.db.QueryRowContext(ctx, `SELECT checkpoint, detail FROM jobs WHERE id = ?`, id).Scan(&ji.Checkpoint, &detail); err != nil {
		return ji, err
	}
	ji.Detail = json.RawMessage(detail)
	return ji, nil
}

// OpenJob returns a VM's queued or running job of a kind, if any.
func (d *DB) OpenJob(ctx context.Context, vmID int64, kind string) (Job, bool, error) {
	j, err := scanJob(d.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE vm_id = ? AND kind = ? AND state IN ('queued', 'running')
		ORDER BY id LIMIT 1`, vmID, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return j, false, nil
	}
	return j, err == nil, err
}

// OpenJobs lists a VM's queued and running jobs of every kind, oldest first.
func (d *DB) OpenJobs(ctx context.Context, vmID int64) ([]Job, error) {
	return rowsOf(ctx, d.db, scanJob, `SELECT `+jobCols+` FROM jobs WHERE vm_id = ? AND state IN ('queued', 'running') ORDER BY id`, vmID)
}

// LastJob returns a VM's newest job of a kind, if any.
func (d *DB) LastJob(ctx context.Context, vmID int64, kind string) (Job, bool, error) {
	j, err := scanJob(d.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE vm_id = ? AND kind = ? ORDER BY id DESC LIMIT 1`, vmID, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return j, false, nil
	}
	return j, err == nil, err
}

// QueuedJobs lists queued jobs, oldest first.
func (d *DB) QueuedJobs(ctx context.Context) ([]Job, error) {
	// (state IN (...) lets SQLite use the partial index jobs_open.)
	return rowsOf(ctx, d.db, scanJob, `SELECT `+jobCols+` FROM jobs WHERE state IN ('queued', 'running') AND state = 'queued' ORDER BY id`)
}

// FailInterruptedJobs ends the jobs a stopped server left running and returns them as they were.
func (d *DB) FailInterruptedJobs(ctx context.Context, reason string) ([]Job, error) {
	js, err := rowsOf(ctx, d.db, scanJob, `SELECT `+jobCols+` FROM jobs WHERE state IN ('queued', 'running') AND state = 'running'`)
	if err != nil {
		return nil, err
	}
	for _, j := range js {
		if _, err := d.SetJobState(ctx, "system", j.ID, "failed", reason, reason); err != nil {
			return nil, err
		}
	}
	return js, nil
}

// FinishInterruptedTests gives every restore test a stopped server left without a verdict the verdict UNKNOWN, with
// the reason: no running test is left behind that would look as if it still ran.
func (d *DB) FinishInterruptedTests(ctx context.Context, reason string) (int, error) {
	ts, err := rowsOf(ctx, d.db, scanTest, `SELECT `+testCols+` FROM restore_tests WHERE finished_at = ''`)
	if err != nil {
		return 0, err
	}
	for _, t := range ts {
		if _, err := d.FinishRestoreTest(ctx, "system", t.ID, "UNKNOWN", nil, "not tested: "+reason, "", map[string]string{"interrupted": reason}); err != nil {
			return 0, err
		}
	}
	return len(ts), nil
}

// FailInterruptedVerifications ends the repository verifications a stopped server left running as failed. A
// verification still running is never started again (the scheduler waits for it), so one left behind would stop all
// later verifications of its repository.
func (d *DB) FailInterruptedVerifications(ctx context.Context, reason string) (int, error) {
	vs, err := rowsOf(ctx, d.db, scanVer, `SELECT `+verCols+` FROM verifications WHERE state = 'running'`)
	if err != nil {
		return 0, err
	}
	for _, v := range vs {
		v.State, v.Error = "failed", reason
		if _, err := d.FinishVerification(ctx, "system", v); err != nil {
			return 0, err
		}
	}
	return len(vs), nil
}

// SyncDisks records the disks a VM has now: new ones are added, sizes updated, removed ones deleted; every change is
// recorded as a state change of the disk.
func (d *DB) SyncDisks(ctx context.Context, actor string, vmID int64, now []DiskInput) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		old, err := rowsOf(ctx, tx, scanDisk, `SELECT id, vm_id, target, size_bytes, created_at, updated_at FROM disks WHERE vm_id = ?`, vmID)
		if err != nil {
			return err
		}
		have := map[string]Disk{}
		for _, k := range old {
			have[k.Target] = k
		}
		ts := d.ts()
		for _, n := range now {
			k, ok := have[n.Target]
			delete(have, n.Target)
			switch {
			case !ok:
				r, err := tx.ExecContext(ctx, `INSERT INTO disks (vm_id, target, size_bytes, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, vmID, n.Target, n.SizeBytes, ts, ts)
				if err != nil {
					return err
				}
				id, err := r.LastInsertId()
				if err != nil {
					return err
				}
				if err := d.recordState(ctx, tx, "disk", id, "", fmt.Sprint(n.SizeBytes), "disk "+n.Target+" found on the host", actor); err != nil {
					return err
				}
			case k.SizeBytes != n.SizeBytes:
				if _, err := tx.ExecContext(ctx, `UPDATE disks SET size_bytes = ?, updated_at = ? WHERE id = ?`, n.SizeBytes, ts, k.ID); err != nil {
					return err
				}
				if err := d.recordState(ctx, tx, "disk", k.ID, fmt.Sprint(k.SizeBytes), fmt.Sprint(n.SizeBytes), "disk "+n.Target+" resized on the host", actor); err != nil {
					return err
				}
			}
		}
		for _, k := range have {
			if _, err := tx.ExecContext(ctx, `DELETE FROM disks WHERE id = ?`, k.ID); err != nil {
				return err
			}
			if err := d.recordState(ctx, tx, "disk", k.ID, fmt.Sprint(k.SizeBytes), "", "disk "+k.Target+" no longer on the host", actor); err != nil {
				return err
			}
		}
		return nil
	})
}

// PinHostKey stores the host key Gaia trusts for a host that has none yet. A host with a pinned key keeps it: a
// different key is only ever an admin's AcceptPresentedHostKey, with a step-up (ADR-007 C17).
func (d *DB) PinHostKey(ctx context.Context, actor string, id int64, key, fingerprint string) (Host, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT ssh_host_key FROM hosts WHERE id = ?`, id).Scan(&old); err != nil {
			return notFound("host", id, err)
		}
		if old == key {
			return nil
		}
		if old != "" {
			return fmt.Errorf("%w: host %d has a pinned SSH key; a different key is accepted only by an admin with a step-up (accept-key)", ErrConflict, id)
		}
		now := d.ts()
		if _, err := tx.ExecContext(ctx, `UPDATE hosts SET ssh_host_key = ?, ssh_host_key_pinned_at = ?, updated_at = ? WHERE id = ?`, key, now, now, id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "host_key", id, "untrusted", "trusted", "host key "+fingerprint+" trusted by "+actor, actor)
	})
	if err != nil {
		return Host{}, err
	}
	return d.Host(ctx, id)
}

// HostKeyPresented records that a host presented an SSH key other than the pinned one (ADR-007 C17). The first time
// a key is seen it is kept, with the time, for an admin to check; the change is a state change (host_key trusted ->
// changed). The same key seen again changes nothing. It never pins anything. True when the key was new.
func (d *DB) HostKeyPresented(ctx context.Context, id int64, key, fingerprint, pinnedFingerprint string) (bool, error) {
	recorded := false
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var pinned, presented string
		if err := tx.QueryRowContext(ctx, `SELECT ssh_host_key, presented_host_key FROM hosts WHERE id = ?`, id).Scan(&pinned, &presented); err != nil {
			return notFound("host", id, err)
		}
		if pinned == "" || key == pinned || key == presented {
			return nil
		}
		from := "trusted"
		if presented != "" {
			from = "changed"
		}
		now := d.ts()
		if _, err := tx.ExecContext(ctx, `UPDATE hosts SET presented_host_key = ?, presented_fingerprint = ?, presented_at = ?, updated_at = ? WHERE id = ?`,
			key, fingerprint, now, now, id); err != nil {
			return err
		}
		recorded = true
		return d.recordState(ctx, tx, "host_key", id, from, "changed", hostKeyChangedReason(fingerprint, pinnedFingerprint), "system")
	})
	return recorded, err
}

// The reason of a host_key change to "changed"; HostKeyChangeOf reads the fingerprints back from it.
const (
	hostKeyChangedPrefix = "the host presented SSH key "
	hostKeyChangedPinned = ", not the pinned "
	hostKeyChangedSuffix = ": Gaia does not connect until an admin accepts it"
)

func hostKeyChangedReason(presented, pinned string) string {
	return hostKeyChangedPrefix + presented + hostKeyChangedPinned + pinned + hostKeyChangedSuffix
}

// HostKeyChange is a host's change of SSH key as a host_key state change to "changed" records it (ADR-007 C17; the
// host_key_changed notification of C18.2): the fingerprints of the key the host presented and of the pinned key it
// is not, and when that pinned key was pinned.
type HostKeyChange struct {
	HostID               int64
	PresentedFingerprint string
	PresentedAt          string // the state change's time: when Gaia first saw the key
	PinnedFingerprint    string
	PinnedAt             string // "" when not known
}

// HostKeyChangeOf reads a host_key state change to "changed". The pinned key's time is the host's pinned time while
// no key was trusted since the change (the host still has that pinned key); after an accepted key, the time of the
// newest host_key change before it that trusted a key ("" when there is none).
func (d *DB) HostKeyChangeOf(ctx context.Context, sc StateChange) (HostKeyChange, error) {
	if sc.Entity != "host_key" || sc.To != "changed" {
		return HostKeyChange{}, fmt.Errorf("%w: state change %d is not a host's changed SSH key (%s %s -> %s)", ErrInvalid, sc.Seq, sc.Entity, sc.From, sc.To)
	}
	rest, ok1 := strings.CutPrefix(sc.Reason, hostKeyChangedPrefix)
	presented, rest, ok2 := strings.Cut(rest, hostKeyChangedPinned)
	pinned, ok3 := strings.CutSuffix(rest, hostKeyChangedSuffix)
	if !ok1 || !ok2 || !ok3 || presented == "" || pinned == "" {
		return HostKeyChange{}, fmt.Errorf("state change %d: the reason %q does not name the presented and the pinned key", sc.Seq, sc.Reason)
	}
	k := HostKeyChange{HostID: sc.EntityID, PresentedFingerprint: presented, PresentedAt: sc.At, PinnedFingerprint: pinned}
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(
		(SELECT NULLIF(ssh_host_key_pinned_at, '') FROM hosts WHERE id = ? AND NOT EXISTS (
			SELECT 1 FROM state_changes WHERE entity = 'host_key' AND entity_id = ? AND to_state = 'trusted' AND seq > ?)),
		(SELECT at FROM state_changes WHERE entity = 'host_key' AND entity_id = ? AND to_state = 'trusted' AND seq < ? ORDER BY seq DESC LIMIT 1),
		'')`, sc.EntityID, sc.EntityID, sc.Seq, sc.EntityID, sc.Seq).Scan(&k.PinnedAt)
	if err != nil {
		return HostKeyChange{}, fmt.Errorf("state change %d: when the pinned key was pinned: %w", sc.Seq, err)
	}
	return k, nil
}

// AcceptPresentedHostKey pins the key a host presented in place of the pinned one, when fingerprint is the presented
// key's: the admin accepts the key they checked, never whatever the host shows now (ADR-007 C17). The change is a
// state change (host_key changed -> trusted) with both fingerprints.
func (d *DB) AcceptPresentedHostKey(ctx context.Context, actor string, id int64, fingerprint, oldFingerprint string) (Host, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var presented, presentedFP string
		if err := tx.QueryRowContext(ctx, `SELECT presented_host_key, presented_fingerprint FROM hosts WHERE id = ?`, id).Scan(&presented, &presentedFP); err != nil {
			return notFound("host", id, err)
		}
		if presented == "" {
			return fmt.Errorf("%w: host %d presented no different SSH key: nothing to accept", ErrConflict, id)
		}
		if fingerprint != presentedFP {
			return fmt.Errorf("%w: the key host %d presented is %s, not %s: not accepted", ErrConflict, id, presentedFP, fingerprint)
		}
		now := d.ts()
		if _, err := tx.ExecContext(ctx, `UPDATE hosts SET ssh_host_key = presented_host_key, ssh_host_key_pinned_at = ?, presented_host_key = '',
			presented_fingerprint = '', presented_at = '', updated_at = ? WHERE id = ?`, now, now, id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "host_key", id, "changed", "trusted",
			"host key "+fingerprint+" accepted by "+actor+" in place of "+oldFingerprint, actor)
	})
	if err != nil {
		return Host{}, err
	}
	return d.Host(ctx, id)
}

// SandboxSlots is the slot count the sandbox helper last reported, and when (ADR-007 C17).
type SandboxSlots struct {
	Slots      int    `json:"slots"`
	ReportedAt string `json:"reported_at"`
}

// SetSandboxSlots records the slot count the sandbox helper reported at a time. A changed count is a state change
// (entity sandbox_slots).
func (d *DB) SetSandboxSlots(ctx context.Context, slots int, at time.Time) error {
	if slots < 0 {
		return fmt.Errorf("%w: slots %d", ErrInvalid, slots)
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		old := -1
		if err := tx.QueryRowContext(ctx, `SELECT slots FROM sandbox_last_known WHERE id = 1`).Scan(&old); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sandbox_last_known (id, slots, reported_at) VALUES (1, ?, ?)
			ON CONFLICT (id) DO UPDATE SET slots = excluded.slots, reported_at = excluded.reported_at`, slots, at.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if old == slots {
			return nil
		}
		from := ""
		if old >= 0 {
			from = fmt.Sprint(old)
		}
		return d.recordState(ctx, tx, "sandbox_slots", 0, from, fmt.Sprint(slots), "the sandbox helper reports "+fmt.Sprint(slots)+" slots", "system")
	})
}

// LastSandboxSlots is the slot count the sandbox helper last reported; false when it never reported one.
func (d *DB) LastSandboxSlots(ctx context.Context) (SandboxSlots, bool, error) {
	var s SandboxSlots
	err := d.db.QueryRowContext(ctx, `SELECT slots, reported_at FROM sandbox_last_known WHERE id = 1`).Scan(&s.Slots, &s.ReportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, false, nil
	}
	return s, err == nil, err
}

// Verification is one repository verification (core matrix #2).
type Verification struct {
	ID            int64           `json:"id"`
	RepositoryID  int64           `json:"repository_id"`
	Mode          string          `json:"mode"`
	State         string          `json:"state"`
	Reason        string          `json:"reason"`
	StartedAt     string          `json:"started_at"`
	FinishedAt    string          `json:"finished_at"`
	PointsChecked int             `json:"points_checked"`
	PointsSkipped int             `json:"points_skipped"` // present points not read because they had failed already
	BlocksRead    int64           `json:"blocks_read"`
	BytesRead     int64           `json:"bytes_read"`
	Affected      json.RawMessage `json:"affected"`
	Error         string          `json:"error"`
}

const verCols = `id, repository_id, mode, state, reason, started_at, finished_at, points_checked, points_skipped, blocks_read, bytes_read, affected, error`

func scanVer(s interface{ Scan(...any) error }) (Verification, error) {
	var v Verification
	var aff string
	err := s.Scan(&v.ID, &v.RepositoryID, &v.Mode, &v.State, &v.Reason, &v.StartedAt, &v.FinishedAt, &v.PointsChecked, &v.PointsSkipped, &v.BlocksRead, &v.BytesRead, &aff, &v.Error)
	v.Affected = json.RawMessage(aff)
	return v, err
}

// StartVerification records a verification that starts.
func (d *DB) StartVerification(ctx context.Context, actor string, repoID int64, mode, reason string) (Verification, error) {
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO verifications (repository_id, mode, state, reason, started_at) VALUES (?, ?, 'running', ?, ?)`, repoID, mode, reason, d.ts())
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "verification", id, "", "running", mode+": "+reason, actor)
	})
	if err != nil {
		return Verification{}, err
	}
	return d.Verification(ctx, id)
}

// FinishVerification records how a verification ended: ok, critical (damaged points, listed in affected), or failed
// (it could not run to the end; errText says why).
func (d *DB) FinishVerification(ctx context.Context, actor string, v Verification) (Verification, error) {
	if len(v.Affected) == 0 {
		v.Affected = json.RawMessage("[]")
	}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE verifications SET state = ?, finished_at = ?, points_checked = ?, points_skipped = ?, blocks_read = ?,
			bytes_read = ?, affected = ?, error = ? WHERE id = ? AND state = 'running'`, v.State, d.ts(), v.PointsChecked, v.PointsSkipped, v.BlocksRead,
			v.BytesRead, string(v.Affected), v.Error, v.ID)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("%w: verification %d is not running", ErrConflict, v.ID)
		}
		reason := fmt.Sprintf("%d points, %d blocks read", v.PointsChecked, v.BlocksRead)
		if v.PointsSkipped > 0 {
			reason += fmt.Sprintf(", %d failed points skipped", v.PointsSkipped)
		}
		if v.Error != "" {
			reason = v.Error
		}
		return d.recordState(ctx, tx, "verification", v.ID, "running", v.State, reason, actor)
	})
	if err != nil {
		return Verification{}, err
	}
	return d.Verification(ctx, v.ID)
}

// Verification returns one verification.
func (d *DB) Verification(ctx context.Context, id int64) (Verification, error) {
	v, err := scanVer(d.db.QueryRowContext(ctx, `SELECT `+verCols+` FROM verifications WHERE id = ?`, id))
	return v, notFound("verification", id, err)
}

// Verifications lists verifications newest first, of one repository if repoID > 0.
func (d *DB) Verifications(ctx context.Context, repoID int64, limit int) ([]Verification, error) {
	if repoID > 0 {
		return rowsOf(ctx, d.db, scanVer, `SELECT `+verCols+` FROM verifications WHERE repository_id = ? ORDER BY id DESC LIMIT ?`, repoID, limit)
	}
	return rowsOf(ctx, d.db, scanVer, `SELECT `+verCols+` FROM verifications ORDER BY id DESC LIMIT ?`, limit)
}

// LastVerification is a repository's newest finished verification of a mode, if any.
func (d *DB) LastVerification(ctx context.Context, repoID int64, mode string) (Verification, bool, error) {
	v, err := scanVer(d.db.QueryRowContext(ctx, `SELECT `+verCols+` FROM verifications WHERE repository_id = ? AND mode = ? AND state != 'running'
		ORDER BY id DESC LIMIT 1`, repoID, mode))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}
