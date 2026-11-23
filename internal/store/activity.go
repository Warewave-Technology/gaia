package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The Activity and Inventory screens of the v2 UI (T27): jobs, hosts and repositories with what their tables show,
// in pages.

// JobStates a job filter takes: one state, or open (queued or running).
var JobStates = []string{"queued", "running", "open", "ok", "failed", "cancelled"}

// JobKinds are the kinds of jobs.
var JobKinds = []string{"backup", "restore_test", "verify_repository", "state_backup", "migration_check"}

// ActivityFilter selects the Activity screen's jobs.
type ActivityFilter struct {
	Kind   string // one of JobKinds, or "" for every kind
	State  string // one of JobStates, or "" for every state
	HostID int64  // the jobs of this host's VMs; 0: every job
	Before int64  // jobs with a smaller id; 0: from the newest
	Limit  int
}

// ActivityRow is a job with what its row shows: the VM and host it is for, who queued it, and what it made.
type ActivityRow struct {
	Job
	VM         string   `json:"vm"` // "" for a job of no VM (a verification, a state backup); a migration check's source VM
	HostID     int64    `json:"host_id"`
	Host       string   `json:"host"`
	By         string   `json:"by"` // who queued it: a username, or "system"
	PointID    *int64   `json:"point_id"`
	PointBytes *int64   `json:"point_bytes"`
	TestID     *int64   `json:"test_id"`
	Verdict    string   `json:"verdict"` // a restore test's or a migration check's
	TestReason string   `json:"test_reason"`
	ProvenRTOS *float64 `json:"proven_rto_s"`
	CheckID    *int64   `json:"migration_check_id"`
}

// Activity lists jobs newest first. The total counts the jobs that match the filter.
func (d *DB) Activity(ctx context.Context, f ActivityFilter) ([]ActivityRow, int, error) {
	var where []string
	var args []any
	if f.Kind != "" {
		if !contains(JobKinds, f.Kind) {
			return nil, 0, fmt.Errorf("%w: kind %q (%s)", ErrInvalid, f.Kind, strings.Join(JobKinds, ", "))
		}
		where, args = append(where, "j.kind = ?"), append(args, f.Kind)
	}
	switch {
	case f.State == "":
	case f.State == "open":
		where = append(where, "j.state IN ('queued', 'running')")
	case contains(JobStates, f.State):
		where, args = append(where, "j.state = ?"), append(args, f.State)
	default:
		return nil, 0, fmt.Errorf("%w: state %q (%s)", ErrInvalid, f.State, strings.Join(JobStates, ", "))
	}
	if f.HostID > 0 {
		where, args = append(where, "j.vm_id IN (SELECT id FROM vms WHERE host_id = ?)"), append(args, f.HostID)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs j`+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	page := append([]string{}, where...)
	pargs := append([]any{}, args...)
	if f.Before > 0 {
		page, pargs = append(page, "j.id < ?"), append(pargs, f.Before)
	}
	cond = ""
	if len(page) > 0 {
		cond = " WHERE " + strings.Join(page, " AND ")
	}
	rows, err := rowsOf(ctx, d.db, scanActivity, `SELECT j.id, j.kind, j.vm_id, j.state, j.reason, j.error, j.created_at, j.started_at, j.finished_at,
		COALESCE(v.name, (SELECT m.vm FROM migration_checks m WHERE m.job_id = j.id), ''), COALESCE(v.host_id, 0), COALESCE(h.name, ''),
		COALESCE((SELECT sc.actor FROM state_changes sc WHERE sc.entity = 'job' AND sc.entity_id = j.id ORDER BY sc.seq LIMIT 1), ''),
		p.id, p.bytes, t.id, COALESCE(t.verdict, m.readiness, ''), COALESCE(t.reason, m.reason, ''), t.proven_rto_s, m.id
		FROM jobs j
		LEFT JOIN vms v ON v.id = j.vm_id
		LEFT JOIN hosts h ON h.id = v.host_id
		LEFT JOIN points p ON p.id = (SELECT p2.id FROM points p2 WHERE p2.job_id = j.id LIMIT 1)
		LEFT JOIN restore_tests t ON t.id = (SELECT t2.id FROM restore_tests t2 WHERE t2.job_id = j.id LIMIT 1)
		LEFT JOIN migration_checks m ON m.id = (SELECT m2.id FROM migration_checks m2 WHERE m2.job_id = j.id LIMIT 1)`+
		cond+` ORDER BY j.id DESC LIMIT ?`, append(pargs, f.Limit)...)
	return rows, total, err
}

func scanActivity(s interface{ Scan(...any) error }) (ActivityRow, error) {
	var r ActivityRow
	var vm, pid, pbytes, tid, cid sql.NullInt64
	var rto sql.NullFloat64
	err := s.Scan(&r.ID, &r.Kind, &vm, &r.State, &r.Reason, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt,
		&r.VM, &r.HostID, &r.Host, &r.By, &pid, &pbytes, &tid, &r.Verdict, &r.TestReason, &rto, &cid)
	if vm.Valid {
		r.VMID = &vm.Int64
	}
	r.PointID, r.PointBytes, r.TestID, r.CheckID = nullInt(pid), nullInt(pbytes), nullInt(tid), nullInt(cid)
	if rto.Valid {
		r.ProvenRTOS = &rto.Float64
	}
	return r, err
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

// FinishedSince counts the jobs that finished at or after a time, by kind and state.
func (d *DB) FinishedSince(ctx context.Context, since string) ([]JobCount, error) {
	return rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (JobCount, error) {
		var c JobCount
		return c, s.Scan(&c.Kind, &c.State, &c.Count)
	}, `SELECT kind, state, COUNT(*) FROM jobs WHERE finished_at != '' AND finished_at >= ? GROUP BY kind, state ORDER BY kind, state`, since)
}

// HostRow is a host with what the Inventory's host table shows: its VMs by status, how many of them cannot be
// backed up because the host does not answer, and its newest point.
type HostRow struct {
	Host
	VMs          int    `json:"vms"`
	Critical     int    `json:"critical"`
	Unknown      int    `json:"unknown"`
	Warning      int    `json:"warning"`
	OK           int    `json:"ok"`
	Unreachable  int    `json:"unreachable"`       // VMs whose status is host_unreachable for this host
	UnreachSince string `json:"unreachable_since"` // the earliest of their since
	LastPointAt  string `json:"last_point_at"`     // the newest point of any of its VMs: the last backup that read it
	// The pinned key's fingerprint, to compare with the presented one, when the host presents a different key
	// (ADR-007 C17); "" otherwise. The view fills it in.
	PinnedFingerprint string `json:"pinned_fingerprint"`
}

// HostRows lists hosts by name, in pages of limit after offset; the total is every host.
func (d *DB) HostRows(ctx context.Context, offset, limit int) ([]HostRow, int, error) {
	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hosts`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (HostRow, error) {
		var r HostRow
		var since, last sql.NullString
		err := s.Scan(append(hostFields(&r.Host), &r.VMs, &r.Critical, &r.Unknown, &r.Warning, &r.OK, &r.Unreachable, &since, &last)...)
		r.UnreachSince, r.LastPointAt = since.String, last.String
		return r, err
	}, `SELECT h.id, h.name, h.address, h.ssh_port, h.ssh_user, h.ssh_host_key, h.ssh_host_key_pinned_at, h.presented_host_key,
		h.presented_fingerprint, h.presented_at, h.created_at, h.updated_at,
		(SELECT COUNT(*) FROM vms v WHERE v.host_id = h.id),
		(SELECT COUNT(*) FROM vms v JOIN vm_status s ON s.vm_id = v.id WHERE v.host_id = h.id AND s.status = 'CRITICAL'),
		(SELECT COUNT(*) FROM vms v LEFT JOIN vm_status s ON s.vm_id = v.id WHERE v.host_id = h.id AND (s.status IS NULL OR s.status = 'UNKNOWN')),
		(SELECT COUNT(*) FROM vms v JOIN vm_status s ON s.vm_id = v.id WHERE v.host_id = h.id AND s.status = 'WARNING'),
		(SELECT COUNT(*) FROM vms v JOIN vm_status s ON s.vm_id = v.id WHERE v.host_id = h.id AND s.status = 'OK'),
		(SELECT COUNT(*) FROM vm_status s WHERE s.code = 'host_unreachable' AND s.scope_kind = 'host' AND s.scope_id = h.id),
		(SELECT MIN(s.changed_at) FROM vm_status s WHERE s.code = 'host_unreachable' AND s.scope_kind = 'host' AND s.scope_id = h.id),
		(SELECT MAX((SELECT MAX(p.taken_at) FROM points p WHERE p.vm_id = v.id)) FROM vms v WHERE v.host_id = h.id)
		FROM hosts h ORDER BY h.name LIMIT ? OFFSET ?`, limit, offset)
	return rows, total, err
}

// RepositoryRow is a repository with what the Inventory's repository table shows: the policies and VMs that use it,
// the contents a verification found damaged, and its newest verification.
type RepositoryRow struct {
	Repository
	Policies         int           `json:"policies"`
	VMs              int           `json:"vms"`
	Quarantined      int           `json:"quarantined_contents"`
	LastVerification *Verification `json:"last_verification"`
}

// RepositoryRows lists repositories by name, in pages; the total is every repository.
func (d *DB) RepositoryRows(ctx context.Context, offset, limit int) ([]RepositoryRow, int, error) {
	var total int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories`).Scan(&total); err != nil {
		return nil, 0, err
	}
	type counts struct {
		id                 int64
		policies, vms, qcs int
	}
	cs, err := rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (counts, error) {
		var c counts
		return c, s.Scan(&c.id, &c.policies, &c.vms, &c.qcs)
	}, `SELECT r.id, (SELECT COUNT(*) FROM policies p WHERE p.repository_id = r.id),
		(SELECT COUNT(*) FROM vms v JOIN policies p ON p.id = v.policy_id WHERE p.repository_id = r.id),
		(SELECT COUNT(*) FROM quarantined_contents q WHERE q.repository_id = r.id)
		FROM repositories r ORDER BY r.name LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := []RepositoryRow{}
	for _, c := range cs {
		r, err := d.Repository(ctx, c.id)
		if err != nil {
			return nil, 0, err
		}
		row := RepositoryRow{Repository: r, Policies: c.policies, VMs: c.vms, Quarantined: c.qcs}
		vs, err := d.Verifications(ctx, c.id, 1)
		if err != nil {
			return nil, 0, err
		}
		if len(vs) == 1 {
			row.LastVerification = &vs[0]
		}
		out = append(out, row)
	}
	return out, total, nil
}
