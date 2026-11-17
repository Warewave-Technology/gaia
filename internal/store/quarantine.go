package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// QuarantinedContent is one damaged repository content a verification took out of deduplication (ADR-007 B3).
type QuarantinedContent struct {
	ID             int64   `json:"id"`
	RepositoryID   int64   `json:"repository_id"`
	VerificationID int64   `json:"verification_id"`
	ContentID      string  `json:"content_id"`
	Pack           string  `json:"pack"`
	Reason         string  `json:"reason"`
	QuarantinedAt  string  `json:"quarantined_at"`
	Manifest       string  `json:"manifest"`
	Points         []int64 `json:"points"`
}

const qcCols = `id, repository_id, verification_id, content_id, pack, reason, quarantined_at, manifest, points`

func scanQC(s interface{ Scan(...any) error }) (QuarantinedContent, error) {
	var q QuarantinedContent
	var pts string
	err := s.Scan(&q.ID, &q.RepositoryID, &q.VerificationID, &q.ContentID, &q.Pack, &q.Reason, &q.QuarantinedAt, &q.Manifest, &pts)
	if err == nil {
		err = json.Unmarshal([]byte(pts), &q.Points)
	}
	return q, err
}

// RecordQuarantine records the contents a verification quarantined, and for each VM in fullReasons that its next
// backup must be full, why. All or nothing.
func (d *DB) RecordQuarantine(ctx context.Context, actor string, repoID, verID int64, manifest string, cs []QuarantinedContent, fullReasons map[int64]string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		now := d.ts()
		for _, c := range cs {
			pts := c.Points
			if pts == nil {
				pts = []int64{}
			}
			b, err := json.Marshal(pts)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO quarantined_contents (repository_id, verification_id, content_id, pack, reason,
				quarantined_at, manifest, points) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, repoID, verID, c.ContentID, c.Pack, c.Reason, now, manifest, string(b)); err != nil {
				return err
			}
		}
		for vmID, reason := range fullReasons {
			if err := setFullBackupReason(ctx, d, tx, actor, vmID, reason, reason); err != nil {
				return err
			}
		}
		return nil
	})
}

func setFullBackupReason(ctx context.Context, d *DB, tx *sql.Tx, actor string, vmID int64, to, why string) error {
	var from string
	if err := tx.QueryRowContext(ctx, `SELECT full_backup_reason FROM vms WHERE id = ?`, vmID).Scan(&from); err != nil {
		return notFound("vm", vmID, err)
	}
	if from == to {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vms SET full_backup_reason = ?, updated_at = ? WHERE id = ?`, to, d.ts(), vmID); err != nil {
		return err
	}
	state := func(r string) string {
		if r == "" {
			return "not required"
		}
		return "required"
	}
	return d.recordState(ctx, tx, "vm_full_backup", vmID, state(from), state(to), why, actor)
}

// ClearFullBackupReason ends a VM's demand for a full backup, saying what met it.
func (d *DB) ClearFullBackupReason(ctx context.Context, actor string, vmID int64, why string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		return setFullBackupReason(ctx, d, tx, actor, vmID, "", why)
	})
}

// QuarantinedContents lists a repository's quarantined contents (all repositories when repoID is 0), oldest first.
func (d *DB) QuarantinedContents(ctx context.Context, repoID int64) ([]QuarantinedContent, error) {
	if repoID > 0 {
		return rowsOf(ctx, d.db, scanQC, `SELECT `+qcCols+` FROM quarantined_contents WHERE repository_id = ? ORDER BY id`, repoID)
	}
	return rowsOf(ctx, d.db, scanQC, `SELECT `+qcCols+` FROM quarantined_contents ORDER BY id`)
}

// Quarantined reports whether a content of a repository was quarantined from the given pack.
func (d *DB) Quarantined(ctx context.Context, repoID int64, contentID, pack string) (bool, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM quarantined_contents WHERE repository_id = ? AND content_id = ? AND pack = ?`,
		repoID, contentID, pack).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("quarantined contents: %w", err)
	}
	return n > 0, nil
}
