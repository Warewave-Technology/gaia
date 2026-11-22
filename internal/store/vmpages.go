package store

import (
	"context"
	"strings"
)

// The VM page's lists in pages, newest first (T27 step 1b, ADR-007 C15): a VM's kept points and its status changes.
// Each page is found by the key of the row before it, so a page costs the same however far back it is.

// KeptPoints lists one page of a VM's present points, newest first: those with an id below before (0: from the newest).
func (d *DB) KeptPoints(ctx context.Context, vmID, before int64, limit int) ([]Point, error) {
	if before <= 0 {
		return rowsOf(ctx, d.db, scanPoint, `SELECT `+pointCols+` FROM points WHERE vm_id = ? AND state = 'present' ORDER BY id DESC LIMIT ?`, vmID, limit)
	}
	return rowsOf(ctx, d.db, scanPoint, `SELECT `+pointCols+` FROM points WHERE vm_id = ? AND state = 'present' AND id < ? ORDER BY id DESC LIMIT ?`,
		vmID, before, limit)
}

// PointCounts counts a VM's present and pruned points.
func (d *DB) PointCounts(ctx context.Context, vmID int64) (present, pruned int, err error) {
	err = d.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(state = 'present'), 0), COALESCE(SUM(state <> 'present'), 0) FROM points WHERE vm_id = ?`, vmID).
		Scan(&present, &pruned)
	return present, pruned, err
}

// TestsOfPoints lists every restore test of the given points, newest first.
func (d *DB) TestsOfPoints(ctx context.Context, pointIDs []int64) ([]RestoreTest, error) {
	if len(pointIDs) == 0 {
		return []RestoreTest{}, nil
	}
	args := make([]any, len(pointIDs))
	for i, id := range pointIDs {
		args[i] = id
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(pointIDs)), ",")
	return rowsOf(ctx, d.db, scanTest, `SELECT `+testCols+` FROM restore_tests WHERE point_id IN (`+marks+`) ORDER BY id DESC`, args...)
}

// StateChangesBefore lists one page of an entity's recorded changes, newest first: those with a seq below before
// (0: from the newest).
func (d *DB) StateChangesBefore(ctx context.Context, entity string, id, before int64, limit int) ([]StateChange, error) {
	q := `SELECT seq, at, entity, entity_id, from_state, to_state, reason, actor, code FROM state_changes WHERE entity = ? AND entity_id = ?`
	args := []any{entity, id}
	if before > 0 {
		q += ` AND seq < ?`
		args = append(args, before)
	}
	rows, err := d.db.QueryContext(ctx, q+` ORDER BY seq DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StateChange{}
	for rows.Next() {
		var c StateChange
		if err := rows.Scan(&c.Seq, &c.At, &c.Entity, &c.EntityID, &c.From, &c.To, &c.Reason, &c.Actor, &c.Code); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StateChangeAfter is the time of an entity's first change after seq ("" when there is none: the change at seq is the
// current state).
func (d *DB) StateChangeAfter(ctx context.Context, entity string, id, seq int64) (string, error) {
	var at string
	err := d.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(at), '') FROM (SELECT at FROM state_changes WHERE entity = ? AND entity_id = ? AND seq > ? ORDER BY seq LIMIT 1)`,
		entity, id, seq).Scan(&at)
	return at, err
}

// CountEntityChanges counts one entity's recorded changes.
func (d *DB) CountEntityChanges(ctx context.Context, entity string, id int64) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM state_changes WHERE entity = ? AND entity_id = ?`, entity, id).Scan(&n)
	return n, err
}
