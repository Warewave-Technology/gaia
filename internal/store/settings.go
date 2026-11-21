package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Setting reads a setting into v; false when it was never set (v keeps its default).
func (d *DB) Setting(ctx context.Context, key string, v any) (bool, error) {
	var s string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(s), v)
}

// SetSetting stores a setting. The change is a state change (entity setting; from and to are the values, which never
// hold a secret), so the daily report lists it (ADR-008 §10).
func (d *DB) SetSetting(ctx context.Context, actor, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&old); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if old == string(b) {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			key, string(b), d.ts(), actor); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "setting", 0, old, string(b), "setting "+key+" changed by "+actor, actor)
	})
}
