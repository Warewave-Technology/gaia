package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/auth"
)

// API tokens for automation (ADR-008 §9). Fixed rules: role operator or auditor, never admin; at most 90 days.
const TokenMaxAge = 90 * 24 * time.Hour

// APIToken is an automation token. The token itself is shown once, when it is made.
type APIToken struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	AllowedAddrs []string `json:"allowed_addrs"` // addresses and CIDR prefixes it may be used from ([] = any)
	CreatedBy    string   `json:"created_by"`
	CreatedAt    string   `json:"created_at"`
	ExpiresAt    string   `json:"expires_at"`
	RevokedAt    string   `json:"revoked_at"`
	RevokedBy    string   `json:"revoked_by"`
	FirstUsedAt  string   `json:"first_used_at"`
	LastUsedAt   string   `json:"last_used_at"`
	UseDay       string   `json:"use_day"`   // the day (UTC) of use_count
	UseCount     int      `json:"use_count"` // uses on use_day
}

const tokenCols = `id, name, role, allowed_addrs, created_by, created_at, expires_at, revoked_at, revoked_by, first_used_at, last_used_at, use_day, use_count`

func scanToken(r interface{ Scan(...any) error }) (APIToken, error) {
	var t APIToken
	var addrs string
	err := r.Scan(&t.ID, &t.Name, &t.Role, &addrs, &t.CreatedBy, &t.CreatedAt, &t.ExpiresAt, &t.RevokedAt, &t.RevokedBy, &t.FirstUsedAt,
		&t.LastUsedAt, &t.UseDay, &t.UseCount)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal([]byte(addrs), &t.AllowedAddrs)
}

// NewAPIToken makes a token.
type NewAPIToken struct {
	Name         string   `json:"name"`
	Role         string   `json:"role"`       // operator or auditor
	ExpiresAt    string   `json:"expires_at"` // RFC 3339, at most 90 days ahead ("" = 90 days)
	AllowedAddrs []string `json:"allowed_addrs,omitempty"`
}

var tokenNameRe = usernameRe

// CreateAPIToken makes a token and returns it with its secret (shown once; only its sha256 is kept).
func (d *DB) CreateAPIToken(ctx context.Context, actor string, in NewAPIToken) (string, APIToken, error) {
	if !tokenNameRe.MatchString(in.Name) {
		return "", APIToken{}, fmt.Errorf("%w: a token's name is 2-64 of a-z 0-9 . _ - and starts with a letter", ErrInvalid)
	}
	if in.Role != RoleOperator && in.Role != RoleAuditor {
		return "", APIToken{}, fmt.Errorf("%w: an API token's role is operator or auditor, never admin (ADR-008 §9)", ErrInvalid)
	}
	now := d.now().UTC()
	exp := now.Add(TokenMaxAge)
	if in.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil {
			return "", APIToken{}, fmt.Errorf("%w: expires_at %q: want RFC 3339", ErrInvalid, in.ExpiresAt)
		}
		if !t.After(now) || t.Sub(now) > TokenMaxAge+time.Minute {
			return "", APIToken{}, fmt.Errorf("%w: an API token expires within 90 days from now", ErrInvalid)
		}
		exp = t.UTC()
	}
	addrs := []string{}
	for _, a := range in.AllowedAddrs {
		p, err := parsePrefix(a)
		if err != nil {
			return "", APIToken{}, fmt.Errorf("%w: allowed address %q: %v", ErrInvalid, a, err)
		}
		addrs = append(addrs, p.String())
	}
	b, _ := json.Marshal(addrs)
	token := auth.NewToken("gaiat_")
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO api_tokens (name, role, token_sha256, allowed_addrs, created_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, in.Name, in.Role, auth.SHA256Hex(token), string(b), actor, now.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "api_token", id, "", "active", fmt.Sprintf("token %s, role %s, until %s, from %v", in.Name, in.Role,
			exp.Format(time.RFC3339), addrs), actor)
	})
	if err != nil {
		return "", APIToken{}, err
	}
	t, err := scanToken(d.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id))
	return token, t, err
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// APITokens lists every token, newest first.
func (d *DB) APITokens(ctx context.Context) ([]APIToken, error) {
	return rowsOf(ctx, d.db, scanToken, `SELECT `+tokenCols+` FROM api_tokens ORDER BY id DESC`)
}

// RevokeAPIToken ends a token.
func (d *DB) RevokeAPIToken(ctx context.Context, actor string, id int64) (APIToken, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		t, err := scanToken(tx.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id))
		if err != nil {
			return notFound("API token", id, sql.ErrNoRows)
		}
		if t.RevokedAt != "" {
			return fmt.Errorf("%w: token %s was revoked at %s", ErrConflict, t.Name, t.RevokedAt)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ?, revoked_by = ? WHERE id = ?`, d.ts(), actor, id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "api_token", id, "active", "revoked", "revoked by "+actor, actor)
	})
	if err != nil {
		return APIToken{}, err
	}
	return scanToken(d.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id))
}

// ErrTokenRefused is the error of a token that exists but may not be used now (expired, revoked, another address).
var ErrTokenRefused = errors.New("the API token may not be used")

// TokenUse is what using a token changed: its first use ever, or the day's count closed by a new day (both go to
// the audit log, ADR-008 §10).
type TokenUse struct {
	First       bool
	ClosedDay   string
	ClosedCount int
}

// UseAPIToken checks a token for a request from addr and counts the use.
func (d *DB) UseAPIToken(ctx context.Context, token, addr string) (APIToken, TokenUse, error) {
	var use TokenUse
	t, err := scanToken(d.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE token_sha256 = ?`, auth.SHA256Hex(token)))
	if errors.Is(err, ErrNotFound) {
		return t, use, ErrNoSession
	} else if err != nil {
		return t, use, err
	}
	now := d.now().UTC()
	switch {
	case t.RevokedAt != "":
		return t, use, fmt.Errorf("%w: token %s was revoked at %s", ErrTokenRefused, t.Name, t.RevokedAt)
	case !now.Before(parseTS(t.ExpiresAt)):
		return t, use, fmt.Errorf("%w: token %s expired at %s", ErrTokenRefused, t.Name, t.ExpiresAt)
	}
	if len(t.AllowedAddrs) > 0 {
		a, err := netip.ParseAddr(addr)
		ok := false
		for _, s := range t.AllowedAddrs {
			if p, perr := netip.ParsePrefix(s); err == nil && perr == nil && p.Contains(a.Unmap()) {
				ok = true
			}
		}
		if !ok {
			return t, use, fmt.Errorf("%w: token %s may not be used from %s", ErrTokenRefused, t.Name, addr)
		}
	}
	ts, day := now.Format(time.RFC3339Nano), now.Format("2006-01-02")
	use.First = t.FirstUsedAt == ""
	if t.UseDay != day && t.UseDay != "" {
		use.ClosedDay, use.ClosedCount = t.UseDay, t.UseCount
	}
	count := t.UseCount + 1
	if t.UseDay != day {
		count = 1
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE api_tokens SET first_used_at = CASE WHEN first_used_at = '' THEN ? ELSE first_used_at END,
		last_used_at = ?, use_day = ?, use_count = ? WHERE id = ?`, ts, ts, day, count, t.ID); err != nil {
		return t, use, err
	}
	t.LastUsedAt, t.UseDay, t.UseCount = ts, day, count
	if use.First {
		t.FirstUsedAt = ts
	}
	return t, use, nil
}
