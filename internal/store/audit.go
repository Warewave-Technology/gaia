package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"

// AuditEntry is one user action.
type AuditEntry struct {
	Seq        int64  `json:"seq"`
	At         string `json:"at"`
	UserID     *int64 `json:"user_id"` // nil: no authenticated user (e.g. a failed login)
	Username   string `json:"username"`
	Role       string `json:"role"`
	Action     string `json:"action"`
	Target     string `json:"target"`
	Outcome    int    `json:"outcome"`
	RemoteAddr string `json:"remote_addr"`
	Detail     string `json:"detail"`
	PrevSHA256 string `json:"prev_sha256"`
	SHA256     string `json:"sha256"`
}

// chainHash is sha256(prev || "\n" || the entry's fields as JSON, without the two hashes).
func chainHash(e AuditEntry, prev string) string {
	e.PrevSHA256, e.SHA256 = "", ""
	b, _ := json.Marshal(e)
	h := sha256.Sum256(append([]byte(prev+"\n"), b...))
	return hex.EncodeToString(h[:])
}

// AppendAudit records a user action at the end of the chain.
func (d *DB) AppendAudit(ctx context.Context, e AuditEntry) (AuditEntry, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var prev string
		err := tx.QueryRowContext(ctx, `SELECT sha256 FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&prev)
		if errors.Is(err, sql.ErrNoRows) {
			prev = zeroHash
		} else if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) + 1 FROM audit_log`).Scan(&e.Seq); err != nil {
			return err
		}
		e.At, e.PrevSHA256 = d.ts(), prev
		e.SHA256 = chainHash(e, prev)
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_log (seq, at, user_id, username, role, action, target, outcome, remote_addr, detail, prev_sha256, sha256)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.Seq, e.At, e.UserID, e.Username, e.Role, e.Action, e.Target, e.Outcome,
			e.RemoteAddr, e.Detail, e.PrevSHA256, e.SHA256)
		return err
	})
	return e, err
}

const auditCols = `seq, at, user_id, username, role, action, target, outcome, remote_addr, detail, prev_sha256, sha256`

func scanAudit(s interface{ Scan(...any) error }) (AuditEntry, error) {
	var e AuditEntry
	var uid sql.NullInt64
	err := s.Scan(&e.Seq, &e.At, &uid, &e.Username, &e.Role, &e.Action, &e.Target, &e.Outcome, &e.RemoteAddr, &e.Detail, &e.PrevSHA256, &e.SHA256)
	if uid.Valid {
		e.UserID = &uid.Int64
	}
	return e, err
}

// Audit lists entries after seq after, oldest first, at most limit.
func (d *DB) Audit(ctx context.Context, after int64, limit int) ([]AuditEntry, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+auditCols+` FROM audit_log WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditCheck is the result of walking the audit chain.
type AuditCheck struct {
	Entries int64  `json:"entries"`
	Intact  bool   `json:"intact"`
	Head    string `json:"head_sha256"`      // the last entry's hash: copy it elsewhere to detect a cut tail
	Broken  int64  `json:"first_broken_seq"` // 0 when intact
	Problem string `json:"problem,omitempty"`
}

// VerifyAudit recomputes the chain from the first entry.
func (d *DB) VerifyAudit(ctx context.Context) (AuditCheck, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+auditCols+` FROM audit_log ORDER BY seq`)
	if err != nil {
		return AuditCheck{}, err
	}
	defer rows.Close()
	c := AuditCheck{Intact: true, Head: zeroHash}
	prev, want := zeroHash, int64(1)
	for rows.Next() {
		e, err := scanAudit(rows)
		if err != nil {
			return c, err
		}
		c.Entries++
		var problem string
		switch {
		case e.Seq != want:
			problem = fmt.Sprintf("seq %d where %d was expected (an entry was removed)", e.Seq, want)
		case e.PrevSHA256 != prev:
			problem = "prev_sha256 does not match the entry before"
		case chainHash(e, prev) != e.SHA256:
			problem = "the entry's hash does not match its fields (it was changed)"
		}
		if problem != "" && c.Intact {
			c.Intact, c.Broken, c.Problem = false, e.Seq, problem
		}
		prev, want, c.Head = e.SHA256, e.Seq+1, e.SHA256
	}
	return c, rows.Err()
}

// StateChange is one recorded change of an entity's state.
type StateChange struct {
	Seq      int64  `json:"seq"`
	At       string `json:"at"`
	Entity   string `json:"entity"`
	EntityID int64  `json:"entity_id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Reason   string `json:"reason"`
	Actor    string `json:"actor"`
}

func (d *DB) recordState(ctx context.Context, tx *sql.Tx, entity string, id int64, from, to, reason, actor string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO state_changes (at, entity, entity_id, from_state, to_state, reason, actor) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ts(), entity, id, from, to, reason, actor)
	return err
}

// StateChanges lists the recorded changes of one entity (entity "" lists all), oldest first.
func (d *DB) StateChanges(ctx context.Context, entity string, id int64, after int64, limit int) ([]StateChange, error) {
	q := `SELECT seq, at, entity, entity_id, from_state, to_state, reason, actor FROM state_changes WHERE seq > ?`
	args := []any{after}
	if entity != "" {
		q += ` AND entity = ? AND entity_id = ?`
		args = append(args, entity, id)
	}
	rows, err := d.db.QueryContext(ctx, q+` ORDER BY seq LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StateChange{}
	for rows.Next() {
		var c StateChange
		if err := rows.Scan(&c.Seq, &c.At, &c.Entity, &c.EntityID, &c.From, &c.To, &c.Reason, &c.Actor); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
