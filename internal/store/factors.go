package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Second factors (ADR-008 §3).
const (
	FactorWebAuthn = "webauthn"
	FactorTOTP     = "totp"
)

// Factor is an enrolled second factor. Its secret (a TOTP seed, a WebAuthn credential) is never in an answer.
type Factor struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	CredentialID string `json:"credential_id,omitempty"` // WebAuthn, base64url
	CreatedAt    string `json:"created_at"`
	LastUsedAt   string `json:"last_used_at"`
	data         string
	lastStep     int64
}

// Data is the factor's secret: the TOTP seed (base32) or the WebAuthn credential (JSON).
func (f Factor) Data() string { return f.data }

// LastStep is the last TOTP time step whose code was accepted.
func (f Factor) LastStep() int64 { return f.lastStep }

const factorCols = `id, user_id, kind, name, credential_id, created_at, last_used_at, data, last_step`

func scanFactor(r interface{ Scan(...any) error }) (Factor, error) {
	var f Factor
	err := r.Scan(&f.ID, &f.UserID, &f.Kind, &f.Name, &f.CredentialID, &f.CreatedAt, &f.LastUsedAt, &f.data, &f.lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// Factors lists a user's factors, oldest first.
func (d *DB) Factors(ctx context.Context, userID int64) ([]Factor, error) {
	return rowsOf(ctx, d.db, scanFactor, `SELECT `+factorCols+` FROM user_factors WHERE user_id = ? ORDER BY id`, userID)
}

// AddFactor enrols a factor. Every live session of the user but keep ends (a factor change, ADR-008 §5); at the first
// enrolment, during sign-in, there is none.
func (d *DB) AddFactor(ctx context.Context, actor string, userID int64, kind, name, credentialID, data string, keep int64) (Factor, error) {
	if kind != FactorWebAuthn && kind != FactorTOTP {
		return Factor{}, fmt.Errorf("%w: factor kind %q", ErrInvalid, kind)
	}
	if name == "" || len(name) > 64 {
		return Factor{}, fmt.Errorf("%w: a factor's name is 1-64 characters", ErrInvalid)
	}
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		u, err := d.userTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		if u.Source != SourceLocal {
			return fmt.Errorf("%w: %s signs in at the identity provider, which asks for the second factor", ErrInvalid, u.Username)
		}
		if kind == FactorTOTP && u.Role == RoleAdmin {
			return fmt.Errorf("%w: an admin's second factor is a security key (WebAuthn) only", ErrInvalid)
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO user_factors (user_id, kind, name, credential_id, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			userID, kind, name, credentialID, data, d.ts())
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		if err := d.recordState(ctx, tx, "user_factor", id, "", "enrolled", fmt.Sprintf("%s %q of %s", kind, name, u.Username), actor); err != nil {
			return err
		}
		return d.endSessionsTx(ctx, tx, `user_id = ? AND id != ?`, []any{userID, keep}, EndFactorChange, actor)
	})
	if err != nil {
		return Factor{}, err
	}
	return scanFactor(d.db.QueryRowContext(ctx, `SELECT `+factorCols+` FROM user_factors WHERE id = ?`, id))
}

// RemoveFactor removes one of a user's factors. A local user keeps at least one, and an admin at least one WebAuthn
// key (ADR-008 §3). Every live session of the user but keep ends.
func (d *DB) RemoveFactor(ctx context.Context, actor string, userID, factorID, keep int64) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		u, err := d.userTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		f, err := scanFactor(tx.QueryRowContext(ctx, `SELECT `+factorCols+` FROM user_factors WHERE id = ? AND user_id = ?`, factorID, userID))
		if err != nil {
			return notFound("factor", factorID, sql.ErrNoRows)
		}
		var left, keys int
		if err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(kind = 'webauthn'), 0) FROM user_factors WHERE user_id = ? AND id != ?`,
			userID, factorID).Scan(&left, &keys); err != nil {
			return err
		}
		if left == 0 {
			return fmt.Errorf("%w: it is the last second factor of %s: enrol another first", ErrConflict, u.Username)
		}
		if u.Role == RoleAdmin && keys == 0 {
			return fmt.Errorf("%w: it is the last security key of %s, an admin: enrol another key first", ErrConflict, u.Username)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_factors WHERE id = ?`, factorID); err != nil {
			return err
		}
		if err := d.recordState(ctx, tx, "user_factor", factorID, "enrolled", "removed", fmt.Sprintf("%s %q of %s", f.Kind, f.Name, u.Username), actor); err != nil {
			return err
		}
		return d.endSessionsTx(ctx, tx, `user_id = ? AND id != ?`, []any{userID, keep}, EndFactorChange, actor)
	})
}

// ResetFactors removes every factor and recovery code of a user (an admin's reset, ADR-008 §3): the user enrols again
// at the next sign-in. All the user's sessions end.
func (d *DB) ResetFactors(ctx context.Context, actor string, userID int64) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		u, err := d.userTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		if u.Source != SourceLocal {
			return fmt.Errorf("%w: %s signs in at the identity provider: reset its factors there", ErrInvalid, u.Username)
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_factors WHERE user_id = ?`, userID).Scan(&n); err != nil {
			return err
		}
		for _, q := range []string{`DELETE FROM user_factors WHERE user_id = ?`, `DELETE FROM recovery_codes WHERE user_id = ?`} {
			if _, err := tx.ExecContext(ctx, q, userID); err != nil {
				return err
			}
		}
		if err := d.recordState(ctx, tx, "user_factors", userID, fmt.Sprintf("%d enrolled", n), "reset", "factors of "+u.Username+" reset: enrolment at the next sign-in", actor); err != nil {
			return err
		}
		return d.endSessionsTx(ctx, tx, `user_id = ?`, []any{userID}, EndFactorChange, actor)
	})
}

// UseTOTP records that a factor's code of step was accepted; false when a code of that step or a later one was
// accepted already (a code is used once, even by two requests at the same moment).
func (d *DB) UseTOTP(ctx context.Context, factorID, step int64) (bool, error) {
	r, err := d.db.ExecContext(ctx, `UPDATE user_factors SET last_step = ?, last_used_at = ? WHERE id = ? AND kind = 'totp' AND last_step < ?`,
		step, d.ts(), factorID, step)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// UpdateWebAuthn stores a WebAuthn credential after a use (its signature counter moves).
func (d *DB) UpdateWebAuthn(ctx context.Context, factorID int64, data string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE user_factors SET data = ?, last_used_at = ? WHERE id = ? AND kind = 'webauthn'`, data, d.ts(), factorID)
	return err
}

// SetRecoveryCodes replaces a user's recovery codes with new ones (their hashes).
func (d *DB) SetRecoveryCodes(ctx context.Context, actor string, userID int64, hashes []string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
			return err
		}
		now := d.ts()
		for _, h := range hashes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_sha256, created_at) VALUES (?, ?, ?)`, userID, h, now); err != nil {
				return err
			}
		}
		return d.recordState(ctx, tx, "recovery_codes", userID, "", fmt.Sprintf("%d new", len(hashes)), "recovery codes made", actor)
	})
}

// UseRecoveryCode spends a recovery code of a user; false when it is not one, or was used. Each use is a state change
// and, through the sign-in that makes it, an audit entry.
func (d *DB) UseRecoveryCode(ctx context.Context, userID int64, hash string) (bool, int, error) {
	used, left := false, 0
	err := d.tx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM recovery_codes WHERE user_id = ? AND code_sha256 = ? AND used_at = ''`, userID, hash).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE id = ?`, d.ts(), id); err != nil {
			return err
		}
		used = true
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = ? AND used_at = ''`, userID).Scan(&left); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "recovery_codes", userID, "", fmt.Sprintf("one used, %d left", left), "recovery code used", "user "+fmt.Sprint(userID))
	})
	return used, left, err
}

// RecoveryCodesLeft counts a user's unused recovery codes.
func (d *DB) RecoveryCodesLeft(ctx context.Context, userID int64) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = ? AND used_at = ''`, userID).Scan(&n)
	return n, err
}
