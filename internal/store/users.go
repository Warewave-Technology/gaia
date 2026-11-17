package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Roles (ADR-007 §8).
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleAuditor  = "auditor"
)

// ValidRole reports whether r is one of the three roles.
func ValidRole(r string) bool { return r == RoleAdmin || r == RoleOperator || r == RoleAuditor }

// argon2id parameters (RFC 9106 §4, the second recommended option, with 64 MiB of memory).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// MinPasswordLength is the shortest password accepted.
const MinPasswordLength = 12

// HashPassword returns the argon2id PHC string of password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword compares password with a PHC string from HashPassword, in constant time.
func CheckPassword(phc, password string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("not an argon2id hash")
	}
	var v int
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, fmt.Errorf("argon2 version %q", parts[2])
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("argon2 parameters %q", parts[3])
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash makes a login for a missing user cost as much as one for a real user.
var dummyHash, _ = HashPassword("gaia: not a password of anyone")

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,63}$`)

func checkPasswordPolicy(p string) error {
	if utf8.RuneCountInString(p) < MinPasswordLength {
		return fmt.Errorf("%w: a password has at least %d characters", ErrInvalid, MinPasswordLength)
	}
	return nil
}

// User is a local user.
type User struct {
	ID                int64  `json:"id"`
	Username          string `json:"username"`
	Role              string `json:"role"`
	Disabled          bool   `json:"disabled"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
	PasswordChangedAt string `json:"password_changed_at"`
	LastLoginAt       string `json:"last_login_at"`
	FailedLogins      int    `json:"failed_logins"` // wrong passwords in a row since the last good one
	LockedUntil       string `json:"locked_until"`  // the end of the current or last lockout ("" = never locked, or unlocked)
	hash              string
}

const userCols = `id, username, role, password_hash, disabled, created_at, updated_at, password_changed_at, last_login_at, failed_logins, locked_until`

func scanUser(s interface{ Scan(...any) error }) (User, error) {
	var u User
	var dis int
	err := s.Scan(&u.ID, &u.Username, &u.Role, &u.hash, &dis, &u.CreatedAt, &u.UpdatedAt, &u.PasswordChangedAt, &u.LastLoginAt, &u.FailedLogins, &u.LockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.Disabled = dis == 1
	return u, err
}

// CreateUser adds an enabled user.
func (d *DB) CreateUser(ctx context.Context, actor, username, role, password string) (User, error) {
	if !usernameRe.MatchString(username) {
		return User{}, fmt.Errorf("%w: a username is 2-64 of a-z 0-9 . _ - and starts with a letter", ErrInvalid)
	}
	if !ValidRole(role) {
		return User{}, fmt.Errorf("%w: role %q (admin, operator or auditor)", ErrInvalid, role)
	}
	if err := checkPasswordPolicy(password); err != nil {
		return User{}, err
	}
	h, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	var id int64
	err = d.tx(ctx, func(tx *sql.Tx) error {
		now := d.ts()
		r, err := tx.ExecContext(ctx, `INSERT INTO users (username, role, password_hash, created_at, updated_at, password_changed_at)
			VALUES (?, ?, ?, ?, ?, ?)`, username, role, h, now, now, now)
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "user", id, "", "enabled", "created with role "+role, actor)
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}

// User returns a user by ID.
func (d *DB) User(ctx context.Context, id int64) (User, error) {
	return scanUser(d.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// Users lists all users.
func (d *DB) Users(ctx context.Context) ([]User, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserPatch changes a user; nil fields stay as they are.
type UserPatch struct {
	Role     *string `json:"role,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
	Password *string `json:"password,omitempty"`
}

// UpdateUser applies p. The last enabled admin cannot be disabled or demoted. Disabling a user or changing their
// password ends their sessions.
func (d *DB) UpdateUser(ctx context.Context, actor string, id int64, p UserPatch) (User, error) {
	var hash string
	if p.Role != nil && !ValidRole(*p.Role) {
		return User{}, fmt.Errorf("%w: role %q (admin, operator or auditor)", ErrInvalid, *p.Role)
	}
	if p.Password != nil {
		if err := checkPasswordPolicy(*p.Password); err != nil {
			return User{}, err
		}
		var err error
		if hash, err = HashPassword(*p.Password); err != nil {
			return User{}, err
		}
	}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
		if err != nil {
			return err
		}
		now := d.ts()
		demote := p.Role != nil && *p.Role != RoleAdmin && u.Role == RoleAdmin
		disable := p.Disabled != nil && *p.Disabled && !u.Disabled
		if (demote || disable) && u.Role == RoleAdmin && !u.Disabled {
			var admins int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&admins); err != nil {
				return err
			}
			if admins <= 1 {
				return fmt.Errorf("%w: %s is the last enabled admin", ErrConflict, u.Username)
			}
		}
		if p.Role != nil && *p.Role != u.Role {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, *p.Role, now, id); err != nil {
				return err
			}
			if err := d.recordState(ctx, tx, "user_role", id, u.Role, *p.Role, "role changed", actor); err != nil {
				return err
			}
		}
		if p.Disabled != nil && *p.Disabled != u.Disabled {
			from, to := "enabled", "disabled"
			if !*p.Disabled {
				from, to = to, from
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, boolInt(*p.Disabled), now, id); err != nil {
				return err
			}
			if err := d.recordState(ctx, tx, "user", id, from, to, "changed by "+actor, actor); err != nil {
				return err
			}
		}
		if p.Password != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?, updated_at = ? WHERE id = ?`,
				hash, now, now, id); err != nil {
				return err
			}
		}
		if disable || p.Password != nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ErrBadLogin is the only error a failed login reveals.
var ErrBadLogin = errors.New("wrong username or password, or the user is disabled")

// Authenticate checks a username and password. A missing user costs the same time as a wrong password.
func (d *DB) Authenticate(ctx context.Context, username, password string) (User, error) {
	u, err := scanUser(d.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
	if errors.Is(err, ErrNotFound) {
		if _, err := CheckPassword(dummyHash, password); err != nil {
			return User{}, err
		}
		return User{}, ErrBadLogin
	}
	if err != nil {
		return User{}, err
	}
	ok, err := CheckPassword(u.hash, password)
	if err != nil {
		return User{}, fmt.Errorf("user %s: stored password hash: %w", username, err)
	}
	if !ok || u.Disabled {
		return User{}, ErrBadLogin
	}
	return u, nil
}

// Session is a login.
type Session struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
	LastUsedAt string `json:"last_used_at"`
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateSession starts a session for u and returns its bearer token (shown once, stored only as a hash).
func (d *DB) CreateSession(ctx context.Context, u User, remote string, ttl time.Duration) (string, Session, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", Session{}, err
	}
	token := "gaia_" + base64.RawURLEncoding.EncodeToString(b)
	now := d.now().UTC()
	s := Session{UserID: u.ID, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(ttl).Format(time.RFC3339Nano),
		LastUsedAt: now.Format(time.RFC3339Nano)}
	err := d.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO sessions (token_sha256, user_id, created_at, expires_at, last_used_at, remote_addr)
			VALUES (?, ?, ?, ?, ?, ?)`, tokenHash(token), u.ID, s.CreatedAt, s.ExpiresAt, s.LastUsedAt, remote)
		if err != nil {
			return err
		}
		if s.ID, err = r.LastInsertId(); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, s.CreatedAt, u.ID)
		return err
	})
	return token, s, err
}

// ErrNoSession means the token is unknown, expired, or its user is disabled.
var ErrNoSession = errors.New("no valid session")

// SessionUser returns the user of a live session and records its use.
func (d *DB) SessionUser(ctx context.Context, token string) (User, Session, error) {
	var s Session
	row := d.db.QueryRowContext(ctx, `SELECT id, user_id, created_at, expires_at, last_used_at FROM sessions WHERE token_sha256 = ?`, tokenHash(token))
	if err := row.Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.ExpiresAt, &s.LastUsedAt); errors.Is(err, sql.ErrNoRows) {
		return User{}, s, ErrNoSession
	} else if err != nil {
		return User{}, s, err
	}
	exp, err := time.Parse(time.RFC3339Nano, s.ExpiresAt)
	if err != nil {
		return User{}, s, err
	}
	if !d.now().Before(exp) {
		if _, err := d.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, s.ID); err != nil {
			return User{}, s, err
		}
		return User{}, s, ErrNoSession
	}
	u, err := d.User(ctx, s.UserID)
	if err != nil {
		return User{}, s, err
	}
	if u.Disabled {
		return User{}, s, ErrNoSession
	}
	s.LastUsedAt = d.ts()
	if _, err := d.db.ExecContext(ctx, `UPDATE sessions SET last_used_at = ? WHERE id = ?`, s.LastUsedAt, s.ID); err != nil {
		return User{}, s, err
	}
	return u, s, nil
}

// DeleteSession ends a session (logout).
func (d *DB) DeleteSession(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// UserByName returns a user by username.
func (d *DB) UserByName(ctx context.Context, username string) (User, error) {
	u, err := scanUser(d.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
	if errors.Is(err, ErrNotFound) {
		return u, fmt.Errorf("%w: user %s", ErrNotFound, username)
	}
	return u, err
}

// Locked reports whether a user is locked out at now, and until when.
func (u User) Locked(now time.Time) (bool, time.Time) {
	if u.LockedUntil == "" {
		return false, time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, u.LockedUntil)
	if err != nil { // unreadable: treat as locked; an admin unlocks
		return true, now.Add(time.Hour)
	}
	return now.Before(t), t
}

// LoginFailed counts a wrong password for a user. Every maxFailures-th failure in a row locks the user out for lockout
// (a state change "user_lockout"); the user and whether this failure locked it are returned.
func (d *DB) LoginFailed(ctx context.Context, id int64, maxFailures int, lockout time.Duration) (User, bool, error) {
	locked := false
	err := d.tx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
		if err != nil {
			return err
		}
		n := u.FailedLogins + 1
		until := u.LockedUntil
		if maxFailures > 0 && n%maxFailures == 0 {
			locked, until = true, d.now().UTC().Add(lockout).Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET failed_logins = ?, locked_until = ? WHERE id = ?`, n, until, id); err != nil {
			return err
		}
		if !locked {
			return nil
		}
		return d.recordState(ctx, tx, "user_lockout", id, "unlocked", "locked until "+until, fmt.Sprintf("%d wrong passwords in a row", n), "system")
	})
	if err != nil {
		return User{}, false, err
	}
	u, err := d.User(ctx, id)
	return u, locked, err
}

// LoginSucceeded resets a user's count of wrong passwords after a good one.
func (d *DB) LoginSucceeded(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = '' WHERE id = ?`, id)
	return err
}

// UnlockUser ends a user's lockout and resets the count of wrong passwords.
func (d *DB) UnlockUser(ctx context.Context, actor string, id int64) (User, error) {
	err := d.tx(ctx, func(tx *sql.Tx) error {
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
		if err != nil {
			return notFound("user", id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = '' WHERE id = ?`, id); err != nil {
			return err
		}
		from := "unlocked"
		if locked, until := u.Locked(d.now()); locked {
			from = "locked until " + until.UTC().Format(time.RFC3339Nano)
		}
		return d.recordState(ctx, tx, "user_lockout", id, from, "unlocked", fmt.Sprintf("unlocked by %s after %d wrong passwords in a row", actor, u.FailedLogins), actor)
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}
