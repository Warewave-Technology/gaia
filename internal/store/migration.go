package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// --- migration checks (schema 8, ADR-003, ADR-005) ---

// MigrationSource is a vSphere endpoint migration checks read VMs from, with read-only calls only. Its password is
// a file under secrets/, never here.
type MigrationSource struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // vsphere
	URL       string `json:"url"`  // e.g. https://esxi.lab/sdk
	Username  string `json:"username"`
	Insecure  bool   `json:"insecure"` // the endpoint's certificate is not verified (a lab ESXi)
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const sourceCols = `id, name, kind, url, username, insecure, created_at, updated_at`

func scanSource(s interface{ Scan(...any) error }) (MigrationSource, error) {
	var m MigrationSource
	err := s.Scan(&m.ID, &m.Name, &m.Kind, &m.URL, &m.Username, &m.Insecure, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

// CreateMigrationSource records a source.
func (d *DB) CreateMigrationSource(ctx context.Context, actor string, m MigrationSource) (MigrationSource, error) {
	if err := checkName("source", m.Name); err != nil {
		return m, err
	}
	if m.Kind != "vsphere" {
		return m, fmt.Errorf("%w: kind %q (vsphere)", ErrInvalid, m.Kind)
	}
	if !strings.HasPrefix(m.URL, "https://") || m.Username == "" {
		return m, fmt.Errorf("%w: url must be https://... and username is required", ErrInvalid)
	}
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		now := d.ts()
		r, err := tx.ExecContext(ctx, `INSERT INTO migration_sources (name, kind, url, username, insecure, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.Name, m.Kind, m.URL, m.Username, m.Insecure, now, now)
		if err != nil {
			return classify(err)
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "migration_source", id, "", "created", m.URL, actor)
	})
	if err != nil {
		return m, err
	}
	return d.MigrationSource(ctx, id)
}

// MigrationSource returns a source.
func (d *DB) MigrationSource(ctx context.Context, id int64) (MigrationSource, error) {
	m, err := scanSource(d.db.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM migration_sources WHERE id = ?`, id))
	return m, notFound("migration source", id, err)
}

// MigrationSources lists sources.
func (d *DB) MigrationSources(ctx context.Context) ([]MigrationSource, error) {
	return rowsOf(ctx, d.db, scanSource, `SELECT `+sourceCols+` FROM migration_sources ORDER BY id`)
}

// DeleteMigrationSource removes a source that no check refers to.
func (d *DB) DeleteMigrationSource(ctx context.Context, actor string, id int64) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		m, err := scanSource(tx.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM migration_sources WHERE id = ?`, id))
		if err != nil {
			return notFound("migration source", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM migration_sources WHERE id = ?`, id); err != nil {
			return classify(err)
		}
		return d.recordState(ctx, tx, "migration_source", id, "created", "removed", m.URL, actor)
	})
}

// MigrationCheck is one check of one VM of a source.
type MigrationCheck struct {
	ID         int64           `json:"id"`
	SourceID   int64           `json:"source_id"`
	VM         string          `json:"vm"`
	JobID      int64           `json:"job_id"`
	Config     json.RawMessage `json:"config"`
	Readiness  string          `json:"readiness"` // "" while it runs; OK, WARNING, CRITICAL, UNKNOWN
	Reason     string          `json:"reason"`
	BundleID   string          `json:"bundle_id"`
	Firmware   string          `json:"firmware"`
	GuestID    string          `json:"guest_id"`
	Detail     json.RawMessage `json:"detail"`
	CreatedAt  string          `json:"created_at"`
	FinishedAt string          `json:"finished_at"`
}

const mcCols = `id, source_id, vm, job_id, config, readiness, reason, bundle_id, firmware, guest_id, detail, created_at, finished_at`

func scanMC(s interface{ Scan(...any) error }) (MigrationCheck, error) {
	var m MigrationCheck
	var cfg, detail string
	err := s.Scan(&m.ID, &m.SourceID, &m.VM, &m.JobID, &cfg, &m.Readiness, &m.Reason, &m.BundleID, &m.Firmware, &m.GuestID, &detail,
		&m.CreatedAt, &m.FinishedAt)
	m.Config, m.Detail = json.RawMessage(cfg), json.RawMessage(detail)
	return m, err
}

// QueueMigrationCheck queues a check of a VM of a source: the check and its job, in one transaction.
func (d *DB) QueueMigrationCheck(ctx context.Context, actor string, sourceID int64, vm string, config json.RawMessage, reason string) (MigrationCheck, error) {
	if vm == "" || len(vm) > 200 {
		return MigrationCheck{}, fmt.Errorf("%w: vm name", ErrInvalid)
	}
	if len(config) == 0 {
		config = json.RawMessage("{}")
	}
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := scanSource(tx.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM migration_sources WHERE id = ?`, sourceID)); err != nil {
			return notFound("migration source", sourceID, err)
		}
		var running int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_checks c JOIN jobs j ON j.id = c.job_id
			WHERE c.source_id = ? AND c.vm = ? AND j.state IN ('queued', 'running')`, sourceID, vm).Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			return fmt.Errorf("%w: a check of %s is already queued or running", ErrConflict, vm)
		}
		now := d.ts()
		r, err := tx.ExecContext(ctx, `INSERT INTO jobs (kind, state, reason, created_at) VALUES ('migration_check', 'queued', ?, ?)`, reason, now)
		if err != nil {
			return err
		}
		jobID, err := r.LastInsertId()
		if err != nil {
			return err
		}
		if err := d.recordState(ctx, tx, "job", jobID, "", "queued", reason, actor); err != nil {
			return err
		}
		r, err = tx.ExecContext(ctx, `INSERT INTO migration_checks (source_id, vm, job_id, config, created_at) VALUES (?, ?, ?, ?, ?)`,
			sourceID, vm, jobID, string(config), now)
		if err != nil {
			return classify(err)
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "migration_check", id, "", "queued", reason, actor)
	})
	if err != nil {
		return MigrationCheck{}, err
	}
	return d.MigrationCheck(ctx, id)
}

// MigrationCheck returns a check.
func (d *DB) MigrationCheck(ctx context.Context, id int64) (MigrationCheck, error) {
	m, err := scanMC(d.db.QueryRowContext(ctx, `SELECT `+mcCols+` FROM migration_checks WHERE id = ?`, id))
	return m, notFound("migration check", id, err)
}

// MigrationCheckByJob returns the check a job runs.
func (d *DB) MigrationCheckByJob(ctx context.Context, jobID int64) (MigrationCheck, error) {
	m, err := scanMC(d.db.QueryRowContext(ctx, `SELECT `+mcCols+` FROM migration_checks WHERE job_id = ?`, jobID))
	return m, notFound("migration check of job", jobID, err)
}

// MigrationChecks lists checks newest first, of one source if sourceID > 0.
func (d *DB) MigrationChecks(ctx context.Context, sourceID, before int64, limit int) ([]MigrationCheck, error) {
	if before <= 0 {
		before = math.MaxInt64
	}
	if sourceID > 0 {
		return rowsOf(ctx, d.db, scanMC, `SELECT `+mcCols+` FROM migration_checks WHERE source_id = ? AND id < ? ORDER BY id DESC LIMIT ?`, sourceID, before, limit)
	}
	return rowsOf(ctx, d.db, scanMC, `SELECT `+mcCols+` FROM migration_checks WHERE id < ? ORDER BY id DESC LIMIT ?`, before, limit)
}

// MigrationCheckFilter is a page of the migration screen's checks (ADR-007 C18.7: paged on the server, rule 15).
type MigrationCheckFilter struct {
	SourceID int64  // only this source's; 0: every source
	VM       string // only this VM's (with SourceID: one VM's history)
	// Result: OK, WARNING, CRITICAL, UNKNOWN (the readiness), or "open" (queued or running: no readiness yet); "" any.
	Result string
	// Latest: only each VM's newest check (the VM's result), worst first, then by VM name. Otherwise every check, newest
	// first.
	Latest bool
	After  int // the offset: a previous page's next
	Limit  int
}

// MigrationResultOpen is a check's result before it has a readiness: queued or running.
const MigrationResultOpen = "open"

// MigrationCheckRow is a check as a list shows it: the check and its job's state.
type MigrationCheckRow struct {
	MigrationCheck
	JobState     string `json:"job_state"`
	JobStartedAt string `json:"job_started_at"`
}

// MigrationCheckPage is one page of the checks that match f, how many match in all, and how many of the checks that
// match every filter but the result have each result (the screen's filter chips count the whole list, not a page).
func (d *DB) MigrationCheckPage(ctx context.Context, f MigrationCheckFilter) ([]MigrationCheckRow, int, map[string]int, error) {
	switch f.Result {
	case "", "OK", "WARNING", "CRITICAL", "UNKNOWN", MigrationResultOpen:
	default:
		return nil, 0, nil, fmt.Errorf("%w: result %q: OK, WARNING, CRITICAL, UNKNOWN or open", ErrInvalid, f.Result)
	}
	if f.Limit <= 0 || f.After < 0 {
		return nil, 0, nil, fmt.Errorf("%w: after %d, limit %d", ErrInvalid, f.After, f.Limit)
	}
	where, args := []string{"1 = 1"}, []any{}
	if f.SourceID > 0 {
		where, args = append(where, "c.source_id = ?"), append(args, f.SourceID)
	}
	if f.VM != "" {
		where, args = append(where, "c.vm = ?"), append(args, f.VM)
	}
	if f.Latest {
		where = append(where, "c.id IN (SELECT MAX(id) FROM migration_checks GROUP BY source_id, vm)")
	}
	base := strings.Join(where, " AND ")
	const result = `CASE WHEN c.readiness = '' THEN 'open' ELSE c.readiness END`
	counts := map[string]int{}
	type count struct {
		result string
		n      int
	}
	cs, err := rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (count, error) {
		var c count
		return c, s.Scan(&c.result, &c.n)
	}, `SELECT `+result+`, COUNT(*) FROM migration_checks c WHERE `+base+` GROUP BY 1`, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	total := 0
	for _, c := range cs {
		counts[c.result] = c.n
		if f.Result == "" || f.Result == c.result {
			total += c.n
		}
	}
	if f.Result != "" {
		base, args = base+" AND "+result+" = ?", append(args, f.Result)
	}
	order := "c.id DESC"
	if f.Latest { // worst first: not ready, warnings, undecided, in progress, ready
		order = `CASE c.readiness WHEN 'CRITICAL' THEN 0 WHEN 'WARNING' THEN 1 WHEN 'UNKNOWN' THEN 2 WHEN '' THEN 3 ELSE 4 END, c.vm, c.source_id, c.id DESC`
	}
	rows, err := rowsOf(ctx, d.db, func(s interface{ Scan(...any) error }) (MigrationCheckRow, error) {
		var r MigrationCheckRow
		var cfg, detail string
		err := s.Scan(&r.ID, &r.SourceID, &r.VM, &r.JobID, &cfg, &r.Readiness, &r.Reason, &r.BundleID, &r.Firmware, &r.GuestID, &detail,
			&r.CreatedAt, &r.FinishedAt, &r.JobState, &r.JobStartedAt)
		r.Config, r.Detail = json.RawMessage(cfg), json.RawMessage(detail)
		return r, err
	}, `SELECT `+mcColsC+`, j.state, j.started_at FROM migration_checks c JOIN jobs j ON j.id = c.job_id WHERE `+base+`
		ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, f.Limit, f.After)...)
	return rows, total, counts, err
}

// SetMigrationCheckSource records what the source says about the VM (firmware, guest OS) once it was read.
func (d *DB) SetMigrationCheckSource(ctx context.Context, id int64, firmware, guestID string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE migration_checks SET firmware = ?, guest_id = ? WHERE id = ?`, firmware, guestID, id)
	return err
}

// FinishMigrationCheck records a check's readiness, reason, bundle and detail.
func (d *DB) FinishMigrationCheck(ctx context.Context, actor string, id int64, readiness, reason, bundleID string, detail any) (MigrationCheck, error) {
	b, err := json.Marshal(detail)
	if err != nil {
		return MigrationCheck{}, err
	}
	err = d.tx(ctx, func(tx *sql.Tx) error {
		m, err := scanMC(tx.QueryRowContext(ctx, `SELECT `+mcCols+` FROM migration_checks WHERE id = ?`, id))
		if err != nil {
			return notFound("migration check", id, err)
		}
		if m.Readiness != "" {
			return fmt.Errorf("%w: migration check %d already has its readiness", ErrConflict, id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE migration_checks SET readiness = ?, reason = ?, bundle_id = ?, detail = ?, finished_at = ? WHERE id = ?`,
			readiness, reason, bundleID, string(b), d.ts(), id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "migration_check", id, "queued", readiness, reason, actor)
	})
	if err != nil {
		return MigrationCheck{}, err
	}
	return d.MigrationCheck(ctx, id)
}

// AddMigrationEvidence records a migration check's sealed bundle.
func (d *DB) AddMigrationEvidence(ctx context.Context, e EvidenceBundle, checkID int64) (EvidenceBundle, error) {
	r, err := d.db.ExecContext(ctx, `INSERT INTO evidence_bundles (bundle_id, migration_check_id, ledger_seq, bundle_sha256, verdict, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, e.BundleID, checkID, e.LedgerSeq, e.BundleSHA256, e.Verdict, d.ts())
	if err != nil {
		return e, classify(err)
	}
	id, err := r.LastInsertId()
	if err != nil {
		return e, err
	}
	return d.Evidence(ctx, id)
}

// FinishInterruptedMigrationChecks gives every check whose job ended without it (the server stopped while it ran)
// the readiness UNKNOWN: an unchecked VM is never shown as ready, nor as still being checked.
func (d *DB) FinishInterruptedMigrationChecks(ctx context.Context, reason string) (int, error) {
	ms, err := rowsOf(ctx, d.db, scanMC, `SELECT `+mcColsC+` FROM migration_checks c JOIN jobs j ON j.id = c.job_id
		WHERE c.readiness = '' AND j.state IN ('failed', 'cancelled')`)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if _, err := d.FinishMigrationCheck(ctx, "system", m.ID, "UNKNOWN", reason, "", map[string]string{"interrupted": reason}); err != nil {
			return 0, err
		}
	}
	return len(ms), nil
}

const mcColsC = `c.id, c.source_id, c.vm, c.job_id, c.config, c.readiness, c.reason, c.bundle_id, c.firmware, c.guest_id, c.detail, c.created_at, c.finished_at`
