package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	return rowsOf(ctx, d.db, scanJob, `SELECT `+jobCols+` FROM jobs WHERE state = 'queued' ORDER BY id`)
}

// FailInterruptedJobs ends the jobs a stopped server left running.
func (d *DB) FailInterruptedJobs(ctx context.Context, reason string) (int, error) {
	js, err := rowsOf(ctx, d.db, scanJob, `SELECT `+jobCols+` FROM jobs WHERE state = 'running'`)
	if err != nil {
		return 0, err
	}
	for _, j := range js {
		if _, err := d.SetJobState(ctx, "system", j.ID, "failed", reason, reason); err != nil {
			return 0, err
		}
	}
	return len(js), nil
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

// PinHostKey stores the host key Gaia trusts for a host.
func (d *DB) PinHostKey(ctx context.Context, actor string, id int64, key, fingerprint string) (Host, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT ssh_host_key FROM hosts WHERE id = ?`, id).Scan(&old); err != nil {
			return notFound("host", id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hosts SET ssh_host_key = ?, updated_at = ? WHERE id = ?`, key, d.ts(), id); err != nil {
			return err
		}
		from := "untrusted"
		if old != "" {
			from = "trusted (replaced)"
		}
		return d.recordState(ctx, tx, "host_key", id, from, "trusted", "host key "+fingerprint+" trusted by "+actor, actor)
	})
	if err != nil {
		return Host{}, err
	}
	return d.Host(ctx, id)
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
