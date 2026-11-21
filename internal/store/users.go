package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/Warewave-Technology/gaia/internal/auth"
)

// Roles (ADR-007 §8).
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleAuditor  = "auditor"
)

// ValidRole reports whether r is one of the three roles.
func ValidRole(r string) bool { return r == RoleAdmin || r == RoleOperator || r == RoleAuditor }

// RoleRank orders the roles (ADR-008 §2: a user with groups for several roles gets the highest).
func RoleRank(r string) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleAuditor:
		return 1
	}
	return 0
}

// User sources (ADR-008 §1).
const (
	SourceLocal = "local"
	SourceOIDC  = "oidc"
)

// AuditorMaxAge is the longest an auditor's account may run before an admin renews it (ADR-008 §8). Fixed.
const AuditorMaxAge = 30 * 24 * time.Hour

// Why a user must set a new password (User.PasswordChange).
const (
	PasswordChangeAdminReset = "admin_reset" // an admin set the password
	PasswordChangeBlocklist  = "blocklist"   // the password is on a newer blocklist
)

// argon2id parameters (RFC 9106 §4, the second recommended option, with 64 MiB of memory).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// MinPasswordLength is the shortest password accepted (ADR-008 §6).
const MinPasswordLength = auth.MinPasswordLength

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

// SetInstance names the installation for the password rules (a password may not contain it, ADR-008 §6).
func (d *DB) SetInstance(name string) { d.instance = name }

// User is a user: a local account, or one that signs in at the IdP (source oidc).
type User struct {
	ID                int64    `json:"id"`
	Username          string   `json:"username"`
	Role              string   `json:"role"`
	Source            string   `json:"source"` // local or oidc
	Disabled          bool     `json:"disabled"`
	BreakGlass        bool     `json:"break_glass"` // may sign in locally while OIDC is on (ADR-008 §1)
	ExpiresAt         string   `json:"expires_at"`  // an auditor's account ends then ("" for the other roles)
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
	PasswordChangedAt string   `json:"password_changed_at"`
	PasswordChange    string   `json:"password_change"` // why a new password is needed first ("" = it is not)
	LastLoginAt       string   `json:"last_login_at"`
	LockedUntil       string   `json:"locked_until"` // the end of the current or last pause after failed sign-ins ("" = none)
	Factors           []string `json:"factors"`      // the kinds of second factor enrolled: webauthn, totp
	OIDCSubject       string   `json:"-"`
	blocklistVersion  string
	hash              string
}

const userCols = `id, username, role, source, password_hash, disabled, break_glass, expires_at, created_at, updated_at,
	password_changed_at, password_change, last_login_at, locked_until, oidc_subject, blocklist_version,
	coalesce((SELECT group_concat(kind) FROM (SELECT DISTINCT kind FROM user_factors f WHERE f.user_id = users.id ORDER BY kind)), '')`

func scanUser(s interface{ Scan(...any) error }) (User, error) {
	var u User
	var dis, bg int
	var factors string
	err := s.Scan(&u.ID, &u.Username, &u.Role, &u.Source, &u.hash, &dis, &bg, &u.ExpiresAt, &u.CreatedAt, &u.UpdatedAt,
		&u.PasswordChangedAt, &u.PasswordChange, &u.LastLoginAt, &u.LockedUntil, &u.OIDCSubject, &u.blocklistVersion, &factors)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.Disabled, u.BreakGlass = dis == 1, bg == 1
	u.Factors = []string{}
	if factors != "" {
		u.Factors = strings.Split(factors, ",")
	}
	return u, err
}

func (d *DB) userTx(ctx context.Context, tx *sql.Tx, id int64) (User, error) {
	u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, ErrNotFound) {
		return u, fmt.Errorf("%w: user %d", ErrNotFound, id)
	}
	return u, err
}

// Expired reports whether the user's account has run out at now (an auditor's, ADR-008 §8).
func (u User) Expired(now time.Time) bool {
	if u.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, u.ExpiresAt)
	return err != nil || !now.Before(t) // unreadable: expired; an admin renews
}

// NewUser creates a local user.
type NewUser struct {
	Username   string `json:"username"`
	Role       string `json:"role"`
	Password   string `json:"password"`
	BreakGlass bool   `json:"break_glass,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"` // auditors: default and at most 30 days from now
}

// checkExpiry checks an auditor's expiry: in the future, at most AuditorMaxAge ahead. "" is the longest allowed.
func (d *DB) checkExpiry(role, at string) (string, error) {
	if role != RoleAuditor {
		if at != "" {
			return "", fmt.Errorf("%w: only an auditor's account has an expiry", ErrInvalid)
		}
		return "", nil
	}
	now := d.now().UTC()
	if at == "" {
		return now.Add(AuditorMaxAge).Format(time.RFC3339Nano), nil
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return "", fmt.Errorf("%w: expires_at %q: want RFC 3339", ErrInvalid, at)
	}
	if !t.After(now) || t.Sub(now) > AuditorMaxAge+time.Minute {
		return "", fmt.Errorf("%w: an auditor's account expires within 30 days from now (expires_at %s)", ErrInvalid, at)
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}

// passwordRules checks a new password against ADR-008 §6, its history included when the user exists.
func (d *DB) passwordRules(ctx context.Context, q querier, userID int64, username, password string) error {
	rules, err := auth.CheckPassword(password, username, d.instance)
	if err != nil {
		return err
	}
	if err := auth.Broken(rules); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if userID == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT password_hash FROM password_history WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, auth.PasswordHistory)
	if err != nil {
		return err
	}
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return err
		}
		hashes = append(hashes, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, h := range hashes {
		if ok, err := CheckPassword(h, password); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %v: not one of your last %d passwords", ErrInvalid, auth.ErrWeakPassword, auth.PasswordHistory)
		}
	}
	return nil
}

// CreateUser adds an enabled local user, with no second factor yet: it enrols one at its first sign-in (ADR-008 §3).
func (d *DB) CreateUser(ctx context.Context, actor string, in NewUser) (User, error) {
	if !usernameRe.MatchString(in.Username) {
		return User{}, fmt.Errorf("%w: a username is 2-64 of a-z 0-9 . _ - and starts with a letter", ErrInvalid)
	}
	if !ValidRole(in.Role) {
		return User{}, fmt.Errorf("%w: role %q (admin, operator or auditor)", ErrInvalid, in.Role)
	}
	if in.BreakGlass && in.Role != RoleAdmin {
		return User{}, fmt.Errorf("%w: only an admin can be break-glass", ErrInvalid)
	}
	exp, err := d.checkExpiry(in.Role, in.ExpiresAt)
	if err != nil {
		return User{}, err
	}
	if err := d.passwordRules(ctx, d.db, 0, in.Username, in.Password); err != nil {
		return User{}, err
	}
	h, err := HashPassword(in.Password)
	if err != nil {
		return User{}, err
	}
	var id int64
	err = d.tx(ctx, func(tx *sql.Tx) error {
		now := d.ts()
		r, err := tx.ExecContext(ctx, `INSERT INTO users (username, role, password_hash, created_at, updated_at, password_changed_at,
			break_glass, expires_at, blocklist_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.Username, in.Role, h, now, now, now, boolInt(in.BreakGlass), exp, auth.BlocklistVersion)
		if err != nil {
			return err
		}
		if id, err = r.LastInsertId(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO password_history (user_id, password_hash, set_at) VALUES (?, ?, ?)`, id, h, now); err != nil {
			return err
		}
		what := "created with role " + in.Role
		if in.BreakGlass {
			what += ", break-glass"
		}
		if exp != "" {
			what += ", expires " + exp
		}
		return d.recordState(ctx, tx, "user", id, "", "enabled", what, actor)
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}

// User returns a user by ID.
func (d *DB) User(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(d.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, ErrNotFound) {
		return u, fmt.Errorf("%w: user %d", ErrNotFound, id)
	}
	return u, err
}

// Users lists all users.
func (d *DB) Users(ctx context.Context) ([]User, error) {
	return rowsOf(ctx, d.db, scanUser, `SELECT `+userCols+` FROM users ORDER BY id`)
}

// UserByName returns a user by username.
func (d *DB) UserByName(ctx context.Context, username string) (User, error) {
	u, err := scanUser(d.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
	if errors.Is(err, ErrNotFound) {
		return u, fmt.Errorf("%w: user %s", ErrNotFound, username)
	}
	return u, err
}

// UserPatch changes a user; nil fields stay as they are.
type UserPatch struct {
	Role       *string `json:"role,omitempty"`
	Disabled   *bool   `json:"disabled,omitempty"`
	Password   *string `json:"password,omitempty"` // an admin's reset: the user must choose a new one at the next sign-in
	BreakGlass *bool   `json:"break_glass,omitempty"`
	ExpiresAt  *string `json:"expires_at,omitempty"` // renews an auditor ("" = 30 days from now)
}

// enabledBreakGlassAdmins counts the enabled admins who may sign in locally while OIDC is on.
func enabledBreakGlassAdmins(ctx context.Context, tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0 AND break_glass = 1 AND source = 'local'`).Scan(&n)
	return n, err
}

// UpdateUser applies an admin's change p. The last enabled admin, and the last enabled break-glass admin, cannot be
// disabled, demoted or (break-glass) unmarked (ADR-008 §1). A role change, disabling and a password reset end every
// session of the user (§5); a password reset also makes the user choose a new password at the next sign-in (§6).
func (d *DB) UpdateUser(ctx context.Context, actor string, id int64, p UserPatch) (User, error) {
	var hash string
	if p.Role != nil && !ValidRole(*p.Role) {
		return User{}, fmt.Errorf("%w: role %q (admin, operator or auditor)", ErrInvalid, *p.Role)
	}
	cur, err := d.User(ctx, id)
	if err != nil {
		return User{}, err
	}
	if p.Password != nil {
		if cur.Source != SourceLocal {
			return User{}, fmt.Errorf("%w: %s signs in at the identity provider and has no password here", ErrInvalid, cur.Username)
		}
		if err := d.passwordRules(ctx, d.db, id, cur.Username, *p.Password); err != nil {
			return User{}, err
		}
		if hash, err = HashPassword(*p.Password); err != nil {
			return User{}, err
		}
	}
	err = d.tx(ctx, func(tx *sql.Tx) error {
		u, err := d.userTx(ctx, tx, id)
		if err != nil {
			return err
		}
		now := d.ts()
		role := u.Role
		if p.Role != nil {
			role = *p.Role
		}
		bg := u.BreakGlass && role == RoleAdmin // a demoted admin is no longer break-glass
		if p.BreakGlass != nil {
			bg = *p.BreakGlass
		}
		if bg && (role != RoleAdmin || u.Source != SourceLocal) {
			return fmt.Errorf("%w: only a local admin can be break-glass", ErrInvalid)
		}
		disabled := u.Disabled || (p.Disabled != nil && *p.Disabled)
		if p.Disabled != nil && !*p.Disabled {
			disabled = false
		}
		wasAdmin := u.Role == RoleAdmin && !u.Disabled
		if wasAdmin && (role != RoleAdmin || disabled) {
			var admins int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&admins); err != nil {
				return err
			}
			if admins <= 1 {
				return fmt.Errorf("%w: %s is the last enabled admin", ErrConflict, u.Username)
			}
		}
		if wasAdmin && u.BreakGlass && u.Source == SourceLocal && (role != RoleAdmin || disabled || !bg) {
			n, err := enabledBreakGlassAdmins(ctx, tx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return fmt.Errorf("%w: %s is the last break-glass admin: Gaia must stay reachable when the identity provider is down", ErrConflict, u.Username)
			}
		}
		ends := ""
		if role != u.Role {
			exp, err := d.checkExpiry(role, "")
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, expires_at = ?, updated_at = ? WHERE id = ?`, role, exp, now, id); err != nil {
				return err
			}
			if err := d.recordState(ctx, tx, "user_role", id, u.Role, role, "role changed", actor); err != nil {
				return err
			}
			ends = EndRoleChange
		}
		if p.ExpiresAt != nil {
			if role != RoleAuditor {
				return fmt.Errorf("%w: only an auditor's account has an expiry", ErrInvalid)
			}
			exp, err := d.checkExpiry(role, *p.ExpiresAt)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET expires_at = ?, updated_at = ? WHERE id = ?`, exp, now, id); err != nil {
				return err
			}
			if err := d.recordState(ctx, tx, "user_expiry", id, u.ExpiresAt, exp, "renewed by "+actor, actor); err != nil {
				return err
			}
		}
		if bg != u.BreakGlass {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET break_glass = ?, updated_at = ? WHERE id = ?`, boolInt(bg), now, id); err != nil {
				return err
			}
			if err := d.recordState(ctx, tx, "user_break_glass", id, fmt.Sprint(u.BreakGlass), fmt.Sprint(bg), "changed by "+actor, actor); err != nil {
				return err
			}
		}
		if disabled != u.Disabled {
			from, to := "enabled", "disabled"
			if !disabled {
				from, to = to, from
			}
			if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ?, updated_at = ? WHERE id = ?`, boolInt(disabled), now, id); err != nil {
				return err
			}
			if err := d.recordState(ctx, tx, "user", id, from, to, "changed by "+actor, actor); err != nil {
				return err
			}
			if disabled {
				ends = EndDisabled
			}
		}
		if p.Password != nil {
			if err := d.setPasswordTx(ctx, tx, id, hash, PasswordChangeAdminReset, actor); err != nil {
				return err
			}
			if ends == "" {
				ends = EndPasswordChange
			}
		}
		if ends != "" {
			return d.endSessionsTx(ctx, tx, `user_id = ?`, []any{id}, ends, actor)
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}

// setPasswordTx stores a new password hash and keeps it in the history (a state change "user_password"). change is
// why the user must set another one before anything else ("" = it need not).
func (d *DB) setPasswordTx(ctx context.Context, tx *sql.Tx, id int64, hash, change, actor string) error {
	now := d.ts()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?, password_change = ?, blocklist_version = ?,
		updated_at = ? WHERE id = ?`, hash, now, change, auth.BlocklistVersion, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO password_history (user_id, password_hash, set_at) VALUES (?, ?, ?)`, id, hash, now); err != nil {
		return err
	}
	// Only the last PasswordHistory are compared; older hashes are not kept.
	if _, err := tx.ExecContext(ctx, `DELETE FROM password_history WHERE user_id = ? AND id NOT IN
		(SELECT id FROM password_history WHERE user_id = ? ORDER BY id DESC LIMIT ?)`, id, id, auth.PasswordHistory); err != nil {
		return err
	}
	to := "set"
	if change != "" {
		to = "set, must change: " + change
	}
	return d.recordState(ctx, tx, "user_password", id, "", to, "password set by "+actor, actor)
}

// ChangeOwnPassword sets a user's new password, chosen by the user (after checking the current one, or at sign-in
// when the password expired or must change). It ends the user's other sessions (keep is the one to keep, 0 = none).
func (d *DB) ChangeOwnPassword(ctx context.Context, id int64, password string, keep int64) (User, error) {
	u, err := d.User(ctx, id)
	if err != nil {
		return User{}, err
	}
	if u.Source != SourceLocal {
		return User{}, fmt.Errorf("%w: %s signs in at the identity provider and has no password here", ErrInvalid, u.Username)
	}
	if err := d.passwordRules(ctx, d.db, id, u.Username, password); err != nil {
		return User{}, err
	}
	h, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	err = d.tx(ctx, func(tx *sql.Tx) error {
		if err := d.setPasswordTx(ctx, tx, id, h, "", u.Username); err != nil {
			return err
		}
		return d.endSessionsTx(ctx, tx, `user_id = ? AND id != ?`, []any{id, keep}, EndPasswordChange, u.Username)
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}

// MustChangePassword marks that a user must set a new password before anything else (the password was found on a
// newer blocklist at sign-in).
func (d *DB) MustChangePassword(ctx context.Context, id int64, why string) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_change = ?, updated_at = ? WHERE id = ?`, why, d.ts(), id); err != nil {
			return err
		}
		return d.recordState(ctx, tx, "user_password", id, "set", "must change: "+why, "the password is on the blocklist "+auth.BlocklistVersion, "system")
	})
}

// NoteBlocklistChecked records that a user's password was checked against the current blocklist and is not on it.
func (d *DB) NoteBlocklistChecked(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, `UPDATE users SET blocklist_version = ? WHERE id = ?`, auth.BlocklistVersion, id)
	return err
}

// BlocklistChecked reports whether the user's password was checked against the embedded blocklist.
func (u User) BlocklistChecked() bool { return u.blocklistVersion == auth.BlocklistVersion }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ErrBadLogin is the only error a failed login reveals.
var ErrBadLogin = errors.New("wrong username or password, or the user is disabled")

// Authenticate checks a username and password. A missing user, or one that signs in at the IdP, costs the same time
// as a wrong password and gives the same answer.
func (d *DB) Authenticate(ctx context.Context, username, password string) (User, error) {
	u, err := scanUser(d.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
	if errors.Is(err, ErrNotFound) || (err == nil && u.Source != SourceLocal) {
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

// Locked reports whether a user is paused after failed sign-ins at now, and until when.
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

// UpsertOIDCUser finds or creates the user of an IdP subject (issuer + " " + sub) and gives it role, the one its groups
// map to (ADR-008 §2). A new user is named after the IdP's preferred_username when that name is free and valid. A role
// change ends the user's sessions (§2, §5). An auditor gets an expiry when it becomes one (§8).
func (d *DB) UpsertOIDCUser(ctx context.Context, subject, name, role string) (User, error) {
	if !ValidRole(role) {
		return User{}, fmt.Errorf("%w: role %q", ErrInvalid, role)
	}
	var id int64
	err := d.tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE oidc_subject = ?`, subject).Scan(&id)
		now := d.ts()
		if errors.Is(err, sql.ErrNoRows) {
			uname := strings.ToLower(name)
			var taken int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE username = ?`, uname).Scan(&taken); err != nil {
				return err
			}
			if !usernameRe.MatchString(uname) || taken > 0 {
				uname = "sso-" + auth.SHA256Hex(subject)[:12]
			}
			exp, err := d.checkExpiry(role, "")
			if err != nil {
				return err
			}
			r, err := tx.ExecContext(ctx, `INSERT INTO users (username, role, source, oidc_subject, password_hash, created_at, updated_at,
				password_changed_at, expires_at) VALUES (?, ?, 'oidc', ?, '', ?, ?, '', ?)`, uname, role, subject, now, now, exp)
			if err != nil {
				return err
			}
			if id, err = r.LastInsertId(); err != nil {
				return err
			}
			return d.recordState(ctx, tx, "user", id, "", "enabled", "first sign-in at the identity provider, role "+role, "oidc")
		}
		if err != nil {
			return err
		}
		u, err := d.userTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if u.Role == role {
			return nil
		}
		exp := u.ExpiresAt
		if role != RoleAuditor {
			exp = ""
		} else if u.Role != RoleAuditor {
			if exp, err = d.checkExpiry(role, ""); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, expires_at = ?, updated_at = ? WHERE id = ?`, role, exp, now, id); err != nil {
			return err
		}
		if err := d.recordState(ctx, tx, "user_role", id, u.Role, role, "the identity provider's groups map to another role", "oidc"); err != nil {
			return err
		}
		return d.endSessionsTx(ctx, tx, `user_id = ?`, []any{id}, EndRoleChange, "oidc")
	})
	if err != nil {
		return User{}, err
	}
	return d.User(ctx, id)
}
