package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// --- recovery kits and state restores (schema 5, ADR-007 B5) ---

// RecoveryKit is the record of one recovery kit the server wrote (never its content).
type RecoveryKit struct {
	Generation   int64    `json:"generation"`
	CreatedAt    string   `json:"created_at"`
	CreatedBy    string   `json:"created_by"`
	Reason       string   `json:"reason"`
	SHA256       string   `json:"sha256"`
	Repositories []string `json:"repositories"`
	ConfirmedAt  string   `json:"confirmed_at"`
	ConfirmedBy  string   `json:"confirmed_by"`
}

const kitCols = `generation, created_at, created_by, reason, sha256, repositories, confirmed_at, confirmed_by`

func scanKit(s interface{ Scan(...any) error }) (RecoveryKit, error) {
	var k RecoveryKit
	var repos string
	if err := s.Scan(&k.Generation, &k.CreatedAt, &k.CreatedBy, &k.Reason, &k.SHA256, &repos, &k.ConfirmedAt, &k.ConfirmedBy); err != nil {
		return k, err
	}
	if err := json.Unmarshal([]byte(repos), &k.Repositories); err != nil {
		return k, fmt.Errorf("recovery kit %d: repositories: %w", k.Generation, err)
	}
	return k, nil
}

// NextKitGeneration is the generation the next kit gets.
func (d *DB) NextKitGeneration(ctx context.Context) (int64, error) {
	var n int64
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation), 0) + 1 FROM recovery_kits`).Scan(&n)
	return n, err
}

// AddRecoveryKit records a kit just written; its generation must be the next one.
func (d *DB) AddRecoveryKit(ctx context.Context, actor string, k RecoveryKit) (RecoveryKit, error) {
	if k.Repositories == nil {
		k.Repositories = []string{}
	}
	repos, err := json.Marshal(k.Repositories)
	if err != nil {
		return k, err
	}
	err = d.tx(ctx, func(tx *sql.Tx) error {
		var next int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation), 0) + 1 FROM recovery_kits`).Scan(&next); err != nil {
			return err
		}
		if k.Generation != next {
			return fmt.Errorf("%w: recovery kit generation %d, the next is %d", ErrConflict, k.Generation, next)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_kits (generation, created_at, created_by, reason, sha256, repositories) VALUES (?, ?, ?, ?, ?, ?)`,
			k.Generation, d.ts(), actor, k.Reason, k.SHA256, string(repos)); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "recovery_kit", k.Generation, "", "not confirmed", k.Reason, actor)
	})
	if err != nil {
		return k, err
	}
	return d.RecoveryKit(ctx, k.Generation)
}

// RecoveryKit returns one kit record.
func (d *DB) RecoveryKit(ctx context.Context, gen int64) (RecoveryKit, error) {
	k, err := scanKit(d.db.QueryRowContext(ctx, `SELECT `+kitCols+` FROM recovery_kits WHERE generation = ?`, gen))
	return k, notFound("recovery kit", gen, err)
}

// RecoveryKits lists the kit records newest first.
func (d *DB) RecoveryKits(ctx context.Context) ([]RecoveryKit, error) {
	return rowsOf(ctx, d.db, scanKit, `SELECT `+kitCols+` FROM recovery_kits ORDER BY generation DESC`)
}

// ConfirmRecoveryKit records that a copy of the newest kit with this sha256 was checked outside the server.
// Confirming an older generation, or a different file, is refused.
func (d *DB) ConfirmRecoveryKit(ctx context.Context, actor string, gen int64, sum string) (RecoveryKit, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var latest int64
		var have, confirmed string
		err := tx.QueryRowContext(ctx, `SELECT generation, sha256, confirmed_at FROM recovery_kits ORDER BY generation DESC LIMIT 1`).Scan(&latest, &have, &confirmed)
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: there is no recovery kit yet", ErrNotFound)
		} else if err != nil {
			return err
		}
		switch {
		case gen != latest:
			return fmt.Errorf("%w: that is recovery kit generation %d; the current one is %d (save it again)", ErrConflict, gen, latest)
		case sum != have:
			return fmt.Errorf("%w: that file is not the server's recovery kit generation %d (sha256 differs)", ErrConflict, gen)
		case confirmed != "":
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE recovery_kits SET confirmed_at = ?, confirmed_by = ? WHERE generation = ?`, d.ts(), actor, gen); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "recovery_kit", gen, "not confirmed", "confirmed", "a copy that opens with the passphrase was checked", actor)
	})
	if err != nil {
		return RecoveryKit{}, err
	}
	return d.RecoveryKit(ctx, gen)
}

// StateRestore is one rebuild of the server from a recovery kit and a state backup.
type StateRestore struct {
	ID                 int64           `json:"id"`
	RestoredAt         string          `json:"restored_at"`
	Tag                string          `json:"tag"`
	StateBackup        string          `json:"state_backup"`
	StateBackupCreated string          `json:"state_backup_created"`
	KitGeneration      int64           `json:"kit_generation"`
	Detail             json.RawMessage `json:"detail"`
}

const restoreCols = `id, restored_at, tag, state_backup, state_backup_created, kit_generation, detail`

func scanRestore(s interface{ Scan(...any) error }) (StateRestore, error) {
	var r StateRestore
	var detail string
	err := s.Scan(&r.ID, &r.RestoredAt, &r.Tag, &r.StateBackup, &r.StateBackupCreated, &r.KitGeneration, &detail)
	r.Detail = json.RawMessage(detail)
	return r, err
}

// AddStateRestore records a rebuild.
func (d *DB) AddStateRestore(ctx context.Context, actor string, r StateRestore) (StateRestore, error) {
	if len(r.Detail) == 0 {
		r.Detail = json.RawMessage(`{}`)
	}
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO state_restores (restored_at, tag, state_backup, state_backup_created, kit_generation, detail) VALUES (?, ?, ?, ?, ?, ?)`,
			d.ts(), r.Tag, r.StateBackup, r.StateBackupCreated, r.KitGeneration, string(r.Detail))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "state_restore", id, "", "restored",
			fmt.Sprintf("rebuilt from state backup %s (%s) and recovery kit generation %d", r.StateBackup, r.StateBackupCreated, r.KitGeneration), actor)
	})
	if err != nil {
		return r, err
	}
	return scanRestore(d.db.QueryRowContext(ctx, `SELECT `+restoreCols+` FROM state_restores WHERE id = ?`, id))
}

// StateRestores lists the rebuilds newest first.
func (d *DB) StateRestores(ctx context.Context) ([]StateRestore, error) {
	return rowsOf(ctx, d.db, scanRestore, `SELECT `+restoreCols+` FROM state_restores ORDER BY id DESC`)
}

// RestoreTag is the tag of the newest rebuild ("" if the server was never rebuilt).
func (d *DB) RestoreTag(ctx context.Context) (string, error) {
	var tag string
	err := d.db.QueryRowContext(ctx, `SELECT tag FROM state_restores ORDER BY id DESC LIMIT 1`).Scan(&tag)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return tag, err
}

// SetRepositoryLocation records that a repository is now reached at another path or URL.
func (d *DB) SetRepositoryLocation(ctx context.Context, actor string, id int64, location, reason string) (Repository, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT location FROM repositories WHERE id = ?`, id).Scan(&old); err != nil {
			return notFound("repository", id, err)
		}
		if old == location {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE repositories SET location = ? WHERE id = ?`, location, id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "repository", id, "location "+old, "location "+location, reason, actor)
	})
	if err != nil {
		return Repository{}, err
	}
	return d.Repository(ctx, id)
}

// StoreIDs are the diskstore point IDs of a repository that the catalog knows, pruned or not.
func (d *DB) StoreIDs(ctx context.Context, repoID int64) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT pd.store_id FROM point_disks pd JOIN points p ON p.id = pd.point_id WHERE p.repository_id = ?`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

// CatalogCounts says how much a catalog holds.
type CatalogCounts struct {
	Hosts           int `json:"hosts"`
	VMs             int `json:"vms"`
	Repositories    int `json:"repositories"`
	Policies        int `json:"policies"`
	Jobs            int `json:"jobs"`
	Points          int `json:"points"`
	PresentPoints   int `json:"present_points"`
	RestoreTests    int `json:"restore_tests"`
	EvidenceBundles int `json:"evidence_bundles"`
	Users           int `json:"users"`
	AuditEntries    int `json:"audit_entries"`
	StateChanges    int `json:"state_changes"`
}

// Counts counts the catalog.
func (d *DB) Counts(ctx context.Context) (CatalogCounts, error) {
	var c CatalogCounts
	for _, q := range []struct {
		dst *int
		sql string
	}{
		{&c.Hosts, `SELECT count(*) FROM hosts`}, {&c.VMs, `SELECT count(*) FROM vms`}, {&c.Repositories, `SELECT count(*) FROM repositories`},
		{&c.Policies, `SELECT count(*) FROM policies`}, {&c.Jobs, `SELECT count(*) FROM jobs`}, {&c.Points, `SELECT count(*) FROM points`},
		{&c.PresentPoints, `SELECT count(*) FROM points WHERE state = 'present'`}, {&c.RestoreTests, `SELECT count(*) FROM restore_tests`},
		{&c.EvidenceBundles, `SELECT count(*) FROM evidence_bundles`}, {&c.Users, `SELECT count(*) FROM users`},
		{&c.AuditEntries, `SELECT count(*) FROM audit_log`}, {&c.StateChanges, `SELECT count(*) FROM state_changes`},
	} {
		if err := d.db.QueryRowContext(ctx, q.sql).Scan(q.dst); err != nil {
			return c, err
		}
	}
	return c, nil
}

// FailInterruptedStateBackups marks state backups still running as failed: the process that ran them is gone.
func (d *DB) FailInterruptedStateBackups(ctx context.Context, actor, reason string) (int, error) {
	bs, err := rowsOf(ctx, d.db, scanSB, `SELECT `+sbCols+` FROM state_backups WHERE state = 'running'`)
	if err != nil {
		return 0, err
	}
	for _, b := range bs {
		if _, err := d.FinishStateBackup(ctx, actor, b.ID, "", 0, "", reason); err != nil {
			return 0, err
		}
	}
	return len(bs), nil
}
