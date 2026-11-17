package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
func (d *DB) MigrationChecks(ctx context.Context, sourceID int64, limit int) ([]MigrationCheck, error) {
	if sourceID > 0 {
		return rowsOf(ctx, d.db, scanMC, `SELECT `+mcCols+` FROM migration_checks WHERE source_id = ? ORDER BY id DESC LIMIT ?`, sourceID, limit)
	}
	return rowsOf(ctx, d.db, scanMC, `SELECT `+mcCols+` FROM migration_checks ORDER BY id DESC LIMIT ?`, limit)
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
