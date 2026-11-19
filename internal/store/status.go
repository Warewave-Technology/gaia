package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// FinishRestoreTest records a test's verdict. The change from UNKNOWN (running) to the verdict is a state change
// with the reason.
func (d *DB) FinishRestoreTest(ctx context.Context, actor string, id int64, verdict string, provenRTO *float64, reason, bundleID string, detail any) (RestoreTest, error) {
	b, err := json.Marshal(detail)
	if err != nil {
		return RestoreTest{}, err
	}
	err = d.tx(ctx, func(tx *sql.Tx) error {
		t, err := scanTest(tx.QueryRowContext(ctx, `SELECT `+testCols+` FROM restore_tests WHERE id = ?`, id))
		if err != nil {
			return notFound("restore test", id, err)
		}
		if t.FinishedAt != "" {
			return fmt.Errorf("%w: restore test %d already finished", ErrConflict, id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE restore_tests SET verdict = ?, proven_rto_s = ?, finished_at = ?, reason = ?, bundle_id = ?, detail = ? WHERE id = ?`,
			verdict, provenRTO, d.ts(), reason, bundleID, string(b), id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "restore_test", id, t.Verdict, verdict, reason, actor)
	})
	if err != nil {
		return RestoreTest{}, err
	}
	return d.RestoreTest(ctx, id)
}

// StatusFacts is what a VM's status is computed from (ADR-007 "The status model").
type StatusFacts struct {
	VM            VM
	Policy        *Policy      // nil: no policy
	NewestChecked *Point       // the newest present point whose check passed
	LatestTest    *RestoreTest // the newest test that reached a verdict (not UNKNOWN)
	LatestPoint   *Point       // its point
	LatestAttempt *RestoreTest // the newest finished test, UNKNOWN included
	AttemptPoint  *Point       // its point
	Passing       *RestoreTest // the newest finished OK test of a point that is still present and not damaged
	PassingPoint  *Point       // its point
	Damaged       []int64      // present points whose check failed
	// What reasons name as their cause (ADR-007 C3).
	HostName       string
	RepositoryID   int64 // the policy's repository; 0 without a policy
	RepositoryName string
	LastBackup     *Job            // the newest finished backup job (ok or failed)
	LastBackupInfo json.RawMessage // its detail
}

// StatusFacts reads the facts for one VM. A passing test only counts while its point can still be restored: a
// pruned point, or one a verification found damaged, proves nothing any more.
func (d *DB) StatusFacts(ctx context.Context, vmID int64) (StatusFacts, error) {
	var f StatusFacts
	vm, err := d.VM(ctx, vmID)
	if err != nil {
		return f, err
	}
	f.VM = vm
	if err := d.db.QueryRowContext(ctx, `SELECT name FROM hosts WHERE id = ?`, vm.HostID).Scan(&f.HostName); err != nil {
		return f, fmt.Errorf("the VM's host %d: %w", vm.HostID, err)
	}
	if vm.PolicyID != nil {
		p, err := d.Policy(ctx, *vm.PolicyID)
		if err != nil {
			return f, err
		}
		f.Policy = &p
		f.RepositoryID = p.RepositoryID
		if err := d.db.QueryRowContext(ctx, `SELECT name FROM repositories WHERE id = ?`, p.RepositoryID).Scan(&f.RepositoryName); err != nil {
			return f, fmt.Errorf("the policy's repository %d: %w", p.RepositoryID, err)
		}
	}
	var id int64
	err = d.db.QueryRowContext(ctx, `SELECT id FROM jobs WHERE vm_id = ? AND kind = 'backup' AND state IN ('ok', 'failed') ORDER BY id DESC LIMIT 1`, vmID).Scan(&id)
	switch {
	case err == nil:
		j, err := d.Job(ctx, id)
		if err != nil {
			return f, err
		}
		ji, err := d.JobInfo(ctx, id)
		if err != nil {
			return f, err
		}
		f.LastBackup, f.LastBackupInfo = &j, ji.Detail
	case !errors.Is(err, sql.ErrNoRows):
		return f, err
	}
	if p, err := d.LatestPoint(ctx, vmID); err == nil {
		f.NewestChecked = &p
	} else if !errors.Is(err, ErrNotFound) {
		return f, err
	}
	one := func(q string) (*RestoreTest, *Point, error) {
		t, err := scanTest(d.db.QueryRowContext(ctx, q, vmID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		p, err := d.Point(ctx, t.PointID)
		if err != nil {
			return nil, nil, err
		}
		return &t, &p, nil
	}
	if f.LatestTest, f.LatestPoint, err = one(`SELECT ` + testCols + ` FROM restore_tests WHERE vm_id = ? AND finished_at != '' AND verdict != 'UNKNOWN' ORDER BY id DESC LIMIT 1`); err != nil {
		return f, err
	}
	if f.LatestAttempt, f.AttemptPoint, err = one(`SELECT ` + testCols + ` FROM restore_tests WHERE vm_id = ? AND finished_at != '' ORDER BY id DESC LIMIT 1`); err != nil {
		return f, err
	}
	if f.Passing, f.PassingPoint, err = one(`SELECT t.` + strings.ReplaceAll(testCols, ", ", ", t.") + ` FROM restore_tests t JOIN points p ON p.id = t.point_id
		WHERE t.vm_id = ? AND t.finished_at != '' AND t.verdict = 'OK' AND p.state = 'present' AND p.check_state != 'failed'
		ORDER BY p.taken_at DESC, t.id DESC LIMIT 1`); err != nil {
		return f, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM points WHERE vm_id = ? AND state = 'present' AND check_state = 'failed' ORDER BY id`, vmID)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return f, err
		}
		f.Damaged = append(f.Damaged, id)
	}
	return f, rows.Err()
}

// VMStatus is the status last computed for a VM.
type VMStatus struct {
	VMID       int64           `json:"vm_id"`
	Status     string          `json:"status"`
	Reason     string          `json:"reason"`
	Result     json.RawMessage `json:"result"`
	ChangedAt  string          `json:"changed_at"`
	ComputedAt string          `json:"computed_at"`
}

// SetVMStatus stores a VM's newly computed status. A change of status is a state change (entity vm_status) with the
// deciding reason; the same status again only updates the figures and the time.
func (d *DB) SetVMStatus(ctx context.Context, vmID int64, status, reason string, result any) (changed bool, err error) {
	b, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	err = d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		err := tx.QueryRowContext(ctx, `SELECT status FROM vm_status WHERE vm_id = ?`, vmID).Scan(&old)
		now := d.ts()
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, `INSERT INTO vm_status (vm_id, status, reason, result, changed_at, computed_at) VALUES (?, ?, ?, ?, ?, ?)`,
				vmID, status, reason, string(b), now, now); err != nil {
				return err
			}
		case err != nil:
			return err
		case old != status:
			if _, err := tx.ExecContext(ctx, `UPDATE vm_status SET status = ?, reason = ?, result = ?, changed_at = ?, computed_at = ? WHERE vm_id = ?`,
				status, reason, string(b), now, now, vmID); err != nil {
				return err
			}
		default:
			_, err := tx.ExecContext(ctx, `UPDATE vm_status SET reason = ?, result = ?, computed_at = ? WHERE vm_id = ?`, reason, string(b), now, vmID)
			return err
		}
		changed = true
		return d.recordState(ctx, tx, "vm_status", vmID, old, status, reason, "system")
	})
	return changed, err
}

// VMStatuses lists the stored statuses, of one VM if vmID > 0.
func (d *DB) VMStatuses(ctx context.Context, vmID int64) ([]VMStatus, error) {
	scan := func(s interface{ Scan(...any) error }) (VMStatus, error) {
		var v VMStatus
		var res string
		err := s.Scan(&v.VMID, &v.Status, &v.Reason, &res, &v.ChangedAt, &v.ComputedAt)
		v.Result = json.RawMessage(res)
		return v, err
	}
	q := `SELECT vm_id, status, reason, result, changed_at, computed_at FROM vm_status`
	if vmID > 0 {
		return rowsOf(ctx, d.db, scan, q+` WHERE vm_id = ?`, vmID)
	}
	return rowsOf(ctx, d.db, scan, q+` ORDER BY vm_id`)
}
