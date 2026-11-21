package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Warewave-Technology/gaia/internal/auth"
)

// Sessions (ADR-008 §5). Fixed limits; the idle and absolute timeouts are settings (settings.go).
const (
	MaxSessionsPerUser = 3                   // a fourth ends the oldest
	DeviceMemory       = 30 * 24 * time.Hour // a device or address not seen for this long makes a sign-in new_device
	FailureWindow      = 15 * time.Minute    // §7
	FailuresToPause    = 10
	PauseFor           = 15 * time.Minute
	FailuresBeforeWait = 2 // the 3rd failure starts the delays
)

// Why a session ended (session_end in the audit log, ADR-008 §10).
const (
	EndSignedOut      = "signed_out"
	EndIdle           = "idle"
	EndAbsolute       = "absolute"
	EndRevoked        = "revoked"
	EndReplaced       = "replaced" // the user's fourth session started
	EndRoleChange     = "role_change"
	EndPasswordChange = "password_change"
	EndFactorChange   = "factor_change"
	EndDisabled       = "disabled"
	EndExpired        = "account_expired"
	EndIdPRemoved     = "idp_no_access" // the IdP says the user is disabled or has no mapped group
	EndServerRebuilt  = "server_rebuilt"
)

// Session is a sign-in, live or ended.
type Session struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	Kind         string `json:"kind"`   // browser or cli
	Method       string `json:"method"` // webauthn, totp, recovery_code, oidc
	BreakGlass   bool   `json:"break_glass"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
	LastSeenAt   string `json:"last_seen_at"`
	AbsoluteAt   string `json:"absolute_at"`
	RemoteAddr   string `json:"remote_addr"`
	UserAgent    string `json:"user_agent"`
	NewDevice    bool   `json:"new_device"`
	IdPState     string `json:"idp_state"` // OIDC: ok or idp_unreachable
	IdPRefreshed string `json:"idp_refreshed_at"`
	EndedAt      string `json:"ended_at"`
	EndReason    string `json:"end_reason"`
	idpRefresh   string
}

// IdPRefreshToken is the OIDC refresh token kept for the 10-minute refresh (never in an answer).
func (s Session) IdPRefreshToken() string { return s.idpRefresh }

const sessionCols = `s.id, s.user_id, u.username, u.role, s.kind, s.method, s.break_glass, s.created_at, s.last_active_at, s.last_seen_at,
	s.absolute_at, s.remote_addr, s.user_agent, s.new_device, s.idp_state, s.idp_refreshed_at, s.idp_refresh_token, s.ended_at, s.end_reason`

func scanSession(r interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var bg, nd int
	err := r.Scan(&s.ID, &s.UserID, &s.Username, &s.Role, &s.Kind, &s.Method, &bg, &s.CreatedAt, &s.LastActiveAt, &s.LastSeenAt,
		&s.AbsoluteAt, &s.RemoteAddr, &s.UserAgent, &nd, &s.IdPState, &s.IdPRefreshed, &s.idpRefresh, &s.EndedAt, &s.EndReason)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNoSession
	}
	s.BreakGlass, s.NewDevice = bg == 1, nd == 1
	return s, err
}

// NewSession starts a session.
type NewSession struct {
	UserID          int64
	Kind            string // browser or cli
	Method          string
	BreakGlass      bool
	RemoteAddr      string
	UserAgent       string
	DeviceID        string // the browser's or CLI's device id ("" = none)
	Absolute        time.Duration
	IdPRefreshToken string
}

// CreateSession starts a session and returns its token (shown once, stored only as a hash). It ends the user's
// oldest live sessions beyond MaxSessionsPerUser, and marks the session new_device when its device or address was not
// seen for the user within DeviceMemory (a user's first sign-in is not new_device: there is nothing to compare with).
func (d *DB) CreateSession(ctx context.Context, n NewSession) (string, Session, error) {
	if n.Kind != "browser" && n.Kind != "cli" {
		return "", Session{}, fmt.Errorf("%w: session kind %q", ErrInvalid, n.Kind)
	}
	token := auth.NewToken("gaia_")
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		now := d.now().UTC()
		ts := now.Format(time.RFC3339Nano)
		since := now.Add(-DeviceMemory).Format(time.RFC3339Nano)
		var known, seenAddr, seenDev int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM signin_devices WHERE user_id = ?`, n.UserID).Scan(&known); err != nil {
			return err
		}
		dev := ""
		if n.DeviceID != "" {
			dev = auth.SHA256Hex(n.DeviceID)
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM signin_devices WHERE user_id = ? AND kind = 'address' AND value = ? AND last_seen_at >= ?`,
			n.UserID, n.RemoteAddr, since).Scan(&seenAddr); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM signin_devices WHERE user_id = ? AND kind = 'device' AND value = ? AND last_seen_at >= ?`,
			n.UserID, dev, since).Scan(&seenDev); err != nil {
			return err
		}
		newDevice := known > 0 && (seenAddr == 0 || (dev != "" && seenDev == 0))
		for kind, v := range map[string]string{"address": n.RemoteAddr, "device": dev} {
			if v == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO signin_devices (user_id, kind, value, last_seen_at) VALUES (?, ?, ?, ?)
				ON CONFLICT (user_id, kind, value) DO UPDATE SET last_seen_at = excluded.last_seen_at`, n.UserID, kind, v, ts); err != nil {
				return err
			}
		}
		idp, refreshed := "", ""
		if n.Method == "oidc" {
			idp, refreshed = "ok", ts
		}
		r, err := tx.ExecContext(ctx, `INSERT INTO sessions (token_sha256, user_id, kind, method, break_glass, created_at, last_active_at, last_seen_at,
			absolute_at, remote_addr, user_agent, new_device, idp_state, idp_refreshed_at, idp_refresh_token)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, auth.SHA256Hex(token), n.UserID, n.Kind, n.Method, boolInt(n.BreakGlass),
			ts, ts, ts, now.Add(n.Absolute).Format(time.RFC3339Nano), n.RemoteAddr, n.UserAgent, boolInt(newDevice), idp, refreshed,
			n.IdPRefreshToken)
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, ts, n.UserID); err != nil {
			return err
		}
		what := fmt.Sprintf("%s session, %s, from %s", n.Kind, n.Method, n.RemoteAddr)
		if newDevice {
			what += ", new device"
		}
		if err := d.recordState(ctx, tx, "session", id, "", "live", what, "user "+fmt.Sprint(n.UserID)); err != nil {
			return err
		}
		return d.endSessionsTx(ctx, tx, `user_id = ? AND id NOT IN (SELECT id FROM sessions WHERE user_id = ? AND ended_at = '' ORDER BY id DESC LIMIT ?)`,
			[]any{n.UserID, n.UserID, MaxSessionsPerUser}, EndReplaced, "system")
	})
	if err != nil {
		return "", Session{}, err
	}
	s, err := d.SessionByID(ctx, id)
	return token, s, err
}

// SessionByID returns a session, live or ended.
func (d *DB) SessionByID(ctx context.Context, id int64) (Session, error) {
	s, err := scanSession(d.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.id = ?`, id))
	if errors.Is(err, ErrNoSession) {
		return s, fmt.Errorf("%w: session %d", ErrNotFound, id)
	}
	return s, err
}

// Sessions lists live sessions (userID 0: everyone's), newest first.
func (d *DB) Sessions(ctx context.Context, userID int64) ([]Session, error) {
	q := `SELECT ` + sessionCols + ` FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.ended_at = ''`
	args := []any{}
	if userID != 0 {
		q += ` AND s.user_id = ?`
		args = append(args, userID)
	}
	return rowsOf(ctx, d.db, scanSession, q+` ORDER BY s.id DESC`, args...)
}

// ErrNoSession means the token is unknown or its session ended; SessionEnded says why when it is known.
var ErrNoSession = errors.New("no valid session")

// SessionEnded is the error of a session that ended: the UI says why ("No activity for 15 minutes").
type SessionEnded struct{ Reason string }

func (e *SessionEnded) Error() string { return "the session ended: " + e.Reason }
func (e *SessionEnded) Unwrap() error { return ErrNoSession }

// SessionLimits are the timeouts in force (settings, ADR-008 §5).
type SessionLimits struct{ Idle time.Duration }

// UseSession returns the user and live session of a token and records its use. A background request (the UI's own
// refresh, X-Gaia-Background) does not move the idle timer; any other does. A session past its idle or absolute
// limit, or whose user is disabled or expired, ends here with that reason.
func (d *DB) UseSession(ctx context.Context, token string, lim SessionLimits, background bool) (User, Session, error) {
	s, err := scanSession(d.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_sha256 = ?`,
		auth.SHA256Hex(token)))
	if err != nil {
		return User{}, s, err
	}
	if s.EndedAt != "" {
		return User{}, s, &SessionEnded{s.EndReason}
	}
	now := d.now().UTC()
	u, err := d.User(ctx, s.UserID)
	if err != nil {
		return User{}, s, err
	}
	reason := ""
	switch {
	case u.Disabled:
		reason = EndDisabled
	case u.Expired(now):
		reason = EndExpired
	case !now.Before(parseTS(s.AbsoluteAt)):
		reason = EndAbsolute
	case !now.Before(parseTS(s.LastActiveAt).Add(lim.Idle)):
		reason = EndIdle
	}
	if reason != "" {
		if err := d.EndSession(ctx, s.ID, reason, "system"); err != nil {
			return User{}, s, err
		}
		return User{}, s, &SessionEnded{reason}
	}
	ts := now.Format(time.RFC3339Nano)
	q := `UPDATE sessions SET last_seen_at = ? WHERE id = ?`
	args := []any{ts, s.ID}
	if !background {
		q = `UPDATE sessions SET last_seen_at = ?, last_active_at = ? WHERE id = ?`
		args = []any{ts, ts, s.ID}
		s.LastActiveAt = ts
	}
	if _, err := d.db.ExecContext(ctx, q, args...); err != nil {
		return User{}, s, err
	}
	s.LastSeenAt = ts
	return u, s, nil
}

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{} // unreadable: in the past, so the session ends
	}
	return t
}

// EndSession ends one live session with a reason (an ended one stays as it is).
func (d *DB) EndSession(ctx context.Context, id int64, reason, actor string) error {
	return d.tx(ctx, func(tx *sql.Tx) error { return d.endSessionsTx(ctx, tx, `id = ?`, []any{id}, reason, actor) })
}

// EndUserSessions ends every live session of a user but keep (0 = none).
func (d *DB) EndUserSessions(ctx context.Context, userID, keep int64, reason, actor string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		return d.endSessionsTx(ctx, tx, `user_id = ? AND id != ?`, []any{userID, keep}, reason, actor)
	})
}

// EndAllSessions signs every user out: a rebuilt server does not honour sign-ins made on the lost one.
func (d *DB) EndAllSessions(ctx context.Context) (int64, error) {
	var n int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE ended_at = ''`).Scan(&n); err != nil {
			return err
		}
		return d.endSessionsTx(ctx, tx, `1 = 1`, nil, EndServerRebuilt, "system")
	})
	return n, err
}

// EndStaleSessions ends the live sessions past their idle or absolute limit, so the list of sessions and the audit
// log show them ended even when no request comes. Failures older than a day are dropped too.
func (d *DB) EndStaleSessions(ctx context.Context, lim SessionLimits) error {
	now := d.now().UTC()
	return d.tx(ctx, func(tx *sql.Tx) error {
		ts := now.Format(time.RFC3339Nano)
		if err := d.endSessionsTx(ctx, tx, `absolute_at <= ?`, []any{ts}, EndAbsolute, "system"); err != nil {
			return err
		}
		if err := d.endSessionsTx(ctx, tx, `last_active_at <= ?`, []any{now.Add(-lim.Idle).Format(time.RFC3339Nano)}, EndIdle, "system"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM signin_failures WHERE at < ?`, now.Add(-24*time.Hour).Format(time.RFC3339Nano))
		return err
	})
}

// endSessionsTx ends the live sessions matching where: each end is a state change and an audit entry with its reason
// (ADR-008 §10).
func (d *DB) endSessionsTx(ctx context.Context, tx *sql.Tx, where string, args []any, reason, actor string) error {
	ss, err := rowsOf(ctx, tx, scanSession, `SELECT `+sessionCols+` FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.ended_at = '' AND s.id IN (SELECT id FROM sessions WHERE `+where+`)`, args...)
	if err != nil {
		return err
	}
	ts := d.ts()
	for _, s := range ss {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET ended_at = ?, end_reason = ?, idp_refresh_token = '' WHERE id = ?`, ts, reason, s.ID); err != nil {
			return err
		}
		if err := d.recordState(ctx, tx, "session", s.ID, "live", reason, fmt.Sprintf("%s session of %s ended: %s", s.Kind, s.Username, reason), actor); err != nil {
			return err
		}
		uid := s.UserID
		if _, err := d.appendAudit(ctx, tx, AuditEntry{UserID: &uid, Username: s.Username, Role: s.Role, Action: "session_end",
			Target: fmt.Sprintf("session:%d", s.ID), Outcome: 200, RemoteAddr: s.RemoteAddr, Detail: "ended: " + reason + " (by " + actor + ")"}); err != nil {
			return err
		}
	}
	return nil
}

// MarkIdP records an OIDC session's refresh: ok (with a new refresh token, "" = keep) or idp_unreachable.
func (d *DB) MarkIdP(ctx context.Context, id int64, state, refreshToken string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT idp_state FROM sessions WHERE id = ?`, id).Scan(&old); err != nil {
			return notFound("session", id, err)
		}
		q := `UPDATE sessions SET idp_state = ?, idp_refreshed_at = ? WHERE id = ?`
		args := []any{state, d.ts(), id}
		if refreshToken != "" {
			q = `UPDATE sessions SET idp_state = ?, idp_refreshed_at = ?, idp_refresh_token = ? WHERE id = ?`
			args = []any{state, d.ts(), refreshToken, id}
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return err
		}
		if old == state {
			return nil
		}
		return d.recordState(ctx, tx, "session_idp", id, old, state, "session refresh at the identity provider", "system")
	})
}

// OIDCSessionsDue lists the live OIDC sessions whose last refresh is older than every.
func (d *DB) OIDCSessionsDue(ctx context.Context, every time.Duration) ([]Session, error) {
	return rowsOf(ctx, d.db, scanSession, `SELECT `+sessionCols+` FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.ended_at = '' AND s.method = 'oidc' AND s.idp_refreshed_at <= ? ORDER BY s.idp_refreshed_at`,
		d.now().UTC().Add(-every).Format(time.RFC3339Nano))
}

// Failures is what the failed sign-ins within FailureWindow say about an attempt (ADR-008 §7).
type Failures struct {
	ByUser, ByAddr     int
	LastUser, LastAddr time.Time
}

// SigninFailures counts the failed sign-ins of a username and of an address within FailureWindow.
func (d *DB) SigninFailures(ctx context.Context, username, addr string) (Failures, error) {
	var f Failures
	since := d.now().UTC().Add(-FailureWindow).Format(time.RFC3339Nano)
	for _, q := range []struct {
		col, v string
		n      *int
		last   *time.Time
	}{{"username", username, &f.ByUser, &f.LastUser}, {"remote_addr", addr, &f.ByAddr, &f.LastAddr}} {
		var last string
		if err := d.db.QueryRowContext(ctx, `SELECT count(*), coalesce(max(at), '') FROM signin_failures WHERE `+q.col+` = ? AND at > ?`,
			q.v, since).Scan(q.n, &last); err != nil {
			return f, err
		}
		if last != "" {
			*q.last = parseTS(last)
		}
	}
	return f, nil
}

// SigninFailed records a failed step of a sign-in. When it is the user's FailuresToPause-th failure within
// FailureWindow and the user exists, the account is paused for PauseFor from every address: a state change
// "user_lockout" (which notifies admins, account_locked) and true.
func (d *DB) SigninFailed(ctx context.Context, username, addr string) (bool, time.Time, error) {
	paused, until := false, time.Time{}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		now := d.now().UTC()
		ts := now.Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO signin_failures (username, remote_addr, at) VALUES (?, ?, ?)`, username, addr, ts); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM signin_failures WHERE username = ? AND at > ?`, username,
			now.Add(-FailureWindow).Format(time.RFC3339Nano)).Scan(&n); err != nil {
			return err
		}
		if n < FailuresToPause || n%FailuresToPause != 0 {
			return nil
		}
		paused, until = true, now.Add(PauseFor)
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) { // an unknown name pauses the same; admins hear of it too
			return d.recordState(ctx, tx, "user_lockout", 0, "unlocked", "locked until "+until.Format(time.RFC3339Nano),
				fmt.Sprintf("%d failed sign-ins within %s for the unknown user name %q, the last from %s", n, FailureWindow, username, addr), "system")
		} else if err != nil {
			return err
		}
		u := until.Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE users SET locked_until = ? WHERE id = ?`, u, id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "user_lockout", id, "unlocked", "locked until "+u,
			fmt.Sprintf("%d failed sign-ins within %s, the last from %s", n, FailureWindow, addr), "system")
	})
	return paused, until, err
}

// UnlockUser ends a user's pause and forgets its failed sign-ins.
func (d *DB) UnlockUser(ctx context.Context, actor string, id int64) (User, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		u, err := d.userTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = '' WHERE id = ?`, id); err != nil {
			return err
		}
		var n int64
		r, err := tx.ExecContext(ctx, `DELETE FROM signin_failures WHERE username = ?`, u.Username)
		if err != nil {
			return err
		}
		if n, err = r.RowsAffected(); err != nil {
			return err
		}
		from := "unlocked"
		if locked, until := u.Locked(d.now()); locked {
			from = "locked until " + until.UTC().Format(time.RFC3339Nano)
		}
		return d.recordState(ctx, tx, "user_lockout", id, from, "unlocked", fmt.Sprintf("unlocked by %s; %d failed sign-ins forgotten", actor, n), actor)
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}

// NoteSignin records a sign-in worth telling admins about (breakglass_signin, new_device_signin): a state change of
// the session, which the notifications pick up.
func (d *DB) NoteSignin(ctx context.Context, sessionID int64, event, reason string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		return d.recordState(ctx, tx, "signin", sessionID, "", event, reason, "system")
	})
}

// SecurityChanges are the state changes since a time (RFC 3339, "" = all) that the daily report lists (ADR-008 §1,
// §10): settings changes, break-glass and new-device sign-ins, paused accounts, API tokens made or revoked.
func (d *DB) SecurityChanges(ctx context.Context, since string) ([]StateChange, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT seq, at, entity, entity_id, from_state, to_state, reason, actor, code FROM state_changes
		WHERE at > ? AND (entity IN ('setting', 'signin', 'api_token', 'user_factors', 'user_role', 'user_break_glass')
			OR (entity = 'user_lockout' AND to_state LIKE 'locked until %')) ORDER BY seq`, since)
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

// LastDailyReportAt is when the newest daily report was made ("" = never).
func (d *DB) LastDailyReportAt(ctx context.Context) (string, error) {
	var at string
	err := d.db.QueryRowContext(ctx, `SELECT coalesce(max(generated_at), '') FROM daily_reports`).Scan(&at)
	return at, err
}
