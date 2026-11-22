package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
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
	HostKeyTrusted bool  // the host has a pinned SSH key (ADR-007 C11: host_key_untrusted when not)
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
	if err := d.db.QueryRowContext(ctx, `SELECT name, ssh_host_key != '' FROM hosts WHERE id = ?`, vm.HostID).Scan(&f.HostName, &f.HostKeyTrusted); err != nil {
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
	// From the present points (newest first) to their passing tests: a few dozen points, not every test.
	if f.Passing, f.PassingPoint, err = one(`SELECT t.` + strings.ReplaceAll(testCols, ", ", ", t.") + ` FROM points p CROSS JOIN restore_tests t ON t.point_id = p.id
		WHERE p.vm_id = ? AND p.state = 'present' AND p.check_state != 'failed' AND t.verdict = 'OK' AND t.finished_at != ''
		ORDER BY p.taken_at DESC, t.id DESC LIMIT 1`); err != nil {
		return f, err
	}
	// (No ORDER BY: SQLite then reads the index on vm_id, state, check_state; the ids are sorted below.)
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM points WHERE vm_id = ? AND state = 'present' AND check_state = 'failed'`, vmID)
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
	slices.Sort(f.Damaged)
	return f, rows.Err()
}

// VMStatus is the status last computed for a VM, with what the views read: the deciding reason's code and scope, and
// the inputs of the time-driven figures (unix seconds), so an age is read at the time it is shown.
type VMStatus struct {
	VMID       int64           `json:"vm_id"`
	Status     string          `json:"status"`
	Reason     string          `json:"reason"`
	Result     json.RawMessage `json:"result"`
	ChangedAt  string          `json:"changed_at"`
	ComputedAt string          `json:"computed_at"`
	StatusRow
}

// StatusRow is what a computation stores besides the status and its result.
type StatusRow struct {
	Rank             int      `json:"rank"` // CRITICAL 0, UNKNOWN 1, WARNING 2, OK 3
	Since            *float64 `json:"since"`
	Code             string   `json:"code"`
	ScopeKind        string   `json:"scope_kind"`
	ScopeID          int64    `json:"scope_id"`
	ScopeName        string   `json:"scope_name"`
	NewestPointID    *int64   `json:"newest_point_id"`
	NewestTakenAt    *float64 `json:"newest_taken_at"`
	ProvenPointID    *int64   `json:"proven_point_id"`
	ProvenTakenAt    *float64 `json:"proven_taken_at"`
	ProvenTestID     *int64   `json:"proven_test_id"`
	ProvenFinishedAt *float64 `json:"proven_finished_at"`
	ProvenRTOS       *float64 `json:"proven_rto_s"`
	RPOTargetS       *int64   `json:"rpo_target_s"`
	RTOTargetS       *int64   `json:"rto_target_s"`
	MaxProofAgeS     *int64   `json:"max_proof_age_s"`
	NextChangeAt     *float64 `json:"next_change_at"`
}

const statusCols = `vm_id, status, reason, result, changed_at, computed_at, rank, since, code, scope_kind, scope_id, scope_name,
	newest_point_id, newest_taken_at, proven_point_id, proven_taken_at, proven_test_id, proven_finished_at, proven_rto_s,
	rpo_target_s, rto_target_s, max_proof_age_s, next_change_at`

func scanStatus(s interface{ Scan(...any) error }) (VMStatus, error) {
	var v VMStatus
	var res string
	err := s.Scan(&v.VMID, &v.Status, &v.Reason, &res, &v.ChangedAt, &v.ComputedAt, &v.Rank, &v.Since, &v.Code, &v.ScopeKind, &v.ScopeID,
		&v.ScopeName, &v.NewestPointID, &v.NewestTakenAt, &v.ProvenPointID, &v.ProvenTakenAt, &v.ProvenTestID, &v.ProvenFinishedAt,
		&v.ProvenRTOS, &v.RPOTargetS, &v.RTOTargetS, &v.MaxProofAgeS, &v.NextChangeAt)
	v.Result = json.RawMessage(res)
	return v, err
}

// StatusWrite is one VM's newly computed status, for SetVMStatuses.
type StatusWrite struct {
	VMID   int64
	Status string
	Reason string
	Result any
	Row    StatusRow
}

// SetVMStatus stores a VM's newly computed status. A change of status is a state change (entity vm_status) with the
// deciding reason; the same status again only updates the figures and the time.
func (d *DB) SetVMStatus(ctx context.Context, vmID int64, status, reason string, result any, row StatusRow) (changed bool, err error) {
	ch, err := d.SetVMStatuses(ctx, []StatusWrite{{VMID: vmID, Status: status, Reason: reason, Result: result, Row: row}})
	if err != nil {
		return false, err
	}
	return ch[0], nil
}

// SetVMStatuses stores many VMs' statuses in one transaction (a full recompute: one commit, not one per VM) and says
// which changed.
func (d *DB) SetVMStatuses(ctx context.Context, ws []StatusWrite) ([]bool, error) {
	changed := make([]bool, len(ws))
	err := d.tx(ctx, func(tx *sql.Tx) error {
		for i, w := range ws {
			c, err := d.setVMStatus(ctx, tx, w)
			if err != nil {
				return fmt.Errorf("VM %d: %w", w.VMID, err)
			}
			changed[i] = c
		}
		return nil
	})
	return changed, err
}

func (d *DB) setVMStatus(ctx context.Context, tx *sql.Tx, w StatusWrite) (bool, error) {
	vmID, status, reason, row := w.VMID, w.Status, w.Reason, w.Row
	b, err := json.Marshal(w.Result)
	if err != nil {
		return false, err
	}
	var old, oldChanged string
	var oldSince *float64
	err = tx.QueryRowContext(ctx, `SELECT status, changed_at, since FROM vm_status WHERE vm_id = ?`, vmID).Scan(&old, &oldChanged, &oldSince)
	t := d.now().UTC()
	now := t.Format(time.RFC3339Nano)
	unix := float64(t.UnixNano()) / 1e9
	set := `rank = ?, code = ?, scope_kind = ?, scope_id = ?, scope_name = ?, newest_point_id = ?, newest_taken_at = ?,
		proven_point_id = ?, proven_taken_at = ?, proven_test_id = ?, proven_finished_at = ?, proven_rto_s = ?, rpo_target_s = ?,
		rto_target_s = ?, max_proof_age_s = ?, next_change_at = ?`
	vals := []any{row.Rank, row.Code, row.ScopeKind, row.ScopeID, row.ScopeName, row.NewestPointID, row.NewestTakenAt,
		row.ProvenPointID, row.ProvenTakenAt, row.ProvenTestID, row.ProvenFinishedAt, row.ProvenRTOS, row.RPOTargetS,
		row.RTOTargetS, row.MaxProofAgeS, row.NextChangeAt}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO vm_status (vm_id, status, reason, result, changed_at, computed_at, since) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			vmID, status, reason, string(b), now, now, unix); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	case old != status:
		if _, err := tx.ExecContext(ctx, `UPDATE vm_status SET status = ?, reason = ?, result = ?, changed_at = ?, computed_at = ?, since = ? WHERE vm_id = ?`,
			status, reason, string(b), now, now, unix, vmID); err != nil {
			return false, err
		}
	default:
		since := oldSince
		if since == nil { // a row from before schema 9
			if c, err := time.Parse(time.RFC3339Nano, oldChanged); err == nil {
				u := float64(c.UnixNano()) / 1e9
				since = &u
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE vm_status SET reason = ?, result = ?, computed_at = ?, since = ?, `+set+` WHERE vm_id = ?`,
			append(append([]any{reason, string(b), now, since}, vals...), vmID)...)
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vm_status SET `+set+` WHERE vm_id = ?`, append(vals, vmID)...); err != nil {
		return false, err
	}
	return true, d.recordStateCode(ctx, tx, "vm_status", vmID, old, status, reason, "system", row.Code)
}

// VMStatuses lists the stored statuses, of one VM if vmID > 0.
func (d *DB) VMStatuses(ctx context.Context, vmID int64) ([]VMStatus, error) {
	q := `SELECT ` + statusCols + ` FROM vm_status`
	if vmID > 0 {
		return rowsOf(ctx, d.db, scanStatus, q+` WHERE vm_id = ?`, vmID)
	}
	return rowsOf(ctx, d.db, scanStatus, q+` ORDER BY vm_id`)
}

// DueStatuses lists the VMs whose stored status time alone has changed by now (unix seconds), and those without one.
func (d *DB) DueStatuses(ctx context.Context, now float64) ([]int64, error) {
	return rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (int64, error) {
		var id int64
		return id, s.Scan(&id)
	}, `SELECT v.id FROM vms v LEFT JOIN vm_status s ON s.vm_id = v.id WHERE s.vm_id IS NULL OR s.next_change_at <= ? ORDER BY v.id`, now)
}
