package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func migrated(t *testing.T) *DB {
	t.Helper()
	d := open(t)
	if _, err := d.Migrate(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMigrationsFromEmpty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, f := range []string{"gaia.db", "gaia.db-wal", "gaia.db-shm"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err == nil && fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", f, fi.Mode())
		}
	}
	// Files left with a wider mode (an older install) are narrowed on the next open.
	d.Close()
	for _, f := range []string{"gaia.db", "gaia.db-wal", "gaia.db-shm"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			os.Chmod(filepath.Join(dir, f), 0o644)
		}
	}
	if d, err = Open(filepath.Join(dir, "gaia.db")); err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, f := range []string{"gaia.db", "gaia.db-wal", "gaia.db-shm"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err == nil && fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v after reopening", f, fi.Mode())
		}
	}
	done, err := d.Migrate(ctx, 0)
	if err != nil || !slices.Equal(done, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}) || Latest() != 12 {
		t.Fatalf("from empty: %v %v (latest %d)", done, err, Latest())
	}
	if done, err := d.Migrate(ctx, 0); err != nil || len(done) != 0 {
		t.Fatalf("again: %v %v", done, err)
	}
	applied, err := d.Applied(ctx)
	if err != nil || len(applied) != 12 || applied[11].Name != "0012_activity.sql" {
		t.Fatalf("applied %+v %v", applied, err)
	}
}

// A database at the previous schema version, with data in it, migrates to the latest: the data is kept and the
// schema ends up exactly as on a database migrated from empty.
// releasedSchema is the schema of the previous release (0.1.2), which this package upgrades (CLAUDE.md rule 12).
const releasedSchema = 10

func TestMigrationFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	old := open(t)
	if _, err := old.Migrate(ctx, releasedSchema); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	h, _ := HashPassword("previous-schema-password")
	if _, err := old.SQL().ExecContext(ctx, `INSERT INTO users (username, role, password_hash, created_at, updated_at, password_changed_at)
		VALUES ('olduser', 'admin', ?, ?, ?, ?)`, h, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := old.SQL().ExecContext(ctx, `INSERT INTO hosts (name, address, ssh_user, created_at, updated_at) VALUES ('kvm1', '10.0.0.1', 'root', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	// Schema 11 (ADR-008): the earlier sessions end, admins become break-glass, auditors get an expiry.
	if _, err := old.SQL().ExecContext(ctx, `INSERT INTO users (username, role, password_hash, created_at, updated_at, password_changed_at)
		VALUES ('oldaudit', 'auditor', ?, ?, ?, ?)`, h, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := old.SQL().ExecContext(ctx, `INSERT INTO sessions (token_sha256, user_id, created_at, expires_at, last_used_at, remote_addr)
		VALUES ('abc', 1, ?, ?, ?, '10.0.0.9')`, now, now, now); err != nil {
		t.Fatal(err)
	}
	done, err := old.Migrate(ctx, 0)
	var want []int
	for v := releasedSchema + 1; v <= Latest(); v++ {
		want = append(want, v)
	}
	if err != nil || !slices.Equal(done, want) {
		t.Fatalf("previous -> latest: %v %v", done, err)
	}
	if _, err := old.Authenticate(ctx, "olduser", "previous-schema-password"); err != nil {
		t.Fatalf("a user from the previous schema: %v", err)
	}
	if u, _ := old.UserByName(ctx, "olduser"); !u.BreakGlass || u.Source != SourceLocal || len(u.Factors) != 0 {
		t.Fatalf("admin after the upgrade %+v", u)
	}
	if u, _ := old.UserByName(ctx, "oldaudit"); u.ExpiresAt == "" || u.Expired(time.Now().Add(29*24*time.Hour)) || !u.Expired(time.Now().Add(31*24*time.Hour)) {
		t.Fatalf("auditor after the upgrade %+v", u)
	}
	var n int
	if err := old.SQL().QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d sessions from the previous schema survived (%v)", n, err)
	}
	if hs, err := old.Hosts(ctx); err != nil || len(hs) != 1 || hs[0].Name != "kvm1" {
		t.Fatalf("hosts %+v %v", hs, err)
	}
	fresh := migrated(t)
	a, _ := old.Schema(ctx)
	b, _ := fresh.Schema(ctx)
	if !slices.Equal(a, b) {
		t.Fatalf("schema after migrating from the previous version differs from a fresh one:\n%s\n---\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
}

// Schema 8 rebuilds jobs to add the kind migration_check: jobs, and the points and restore tests that refer to them,
// come through unchanged, and the foreign keys still hold.
func TestMigrationRebuildsJobs(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.Migrate(ctx, 7); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, q := range []string{
		`INSERT INTO hosts (id, name, address, ssh_user, created_at, updated_at) VALUES (1, 'kvm1', '10.0.0.1', 'gaia', '` + now + `', '` + now + `')`,
		`INSERT INTO repositories (id, name, kind, location, splitter, hash, created_at) VALUES (1, 'main', 'filesystem', '/srv', 'FIXED-1M', 'BLAKE2B-256-128', '` + now + `')`,
		`INSERT INTO vms (id, host_id, name, created_at, updated_at) VALUES (1, 1, 'web', '` + now + `', '` + now + `')`,
		`INSERT INTO jobs (id, kind, vm_id, state, reason, created_at, checkpoint, detail) VALUES (7, 'backup', 1, 'ok', 'schedule', '` + now + `', 'gaia-7', '{"bytes":1}')`,
		`INSERT INTO jobs (id, kind, vm_id, state, reason, created_at) VALUES (8, 'restore_test', 1, 'ok', 'schedule', '` + now + `')`,
		`INSERT INTO points (id, vm_id, job_id, repository_id, kind, taken_at, check_state) VALUES (3, 1, 7, 1, 'full', '` + now + `', 'ok')`,
		`INSERT INTO restore_tests (id, vm_id, point_id, job_id, verdict, started_at) VALUES (4, 1, 3, 8, 'OK', '` + now + `')`,
	} {
		if _, err := d.SQL().ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := d.Migrate(ctx, 0); err != nil {
		t.Fatal(err)
	}
	var kind, cp, detail string
	if err := d.SQL().QueryRowContext(ctx, `SELECT kind, checkpoint, detail FROM jobs WHERE id = 7`).Scan(&kind, &cp, &detail); err != nil ||
		kind != "backup" || cp != "gaia-7" || detail != `{"bytes":1}` {
		t.Fatalf("job 7 after the rebuild: %s %s %s %v", kind, cp, detail, err)
	}
	var n int
	if err := d.SQL().QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("foreign key violations: %d %v", n, err)
	}
	if mj, err := d.CreateJob(ctx, "admin", "migration_check", nil, "test"); err != nil || mj.ID != 9 {
		t.Fatalf("a migration_check job: %+v %v", mj, err)
	}
	if _, err := d.SQL().ExecContext(ctx, `DELETE FROM jobs WHERE id = 7`); err == nil {
		t.Fatal("deleted a job a point refers to: the foreign key is gone")
	}
}

func TestMigrationGuards(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	if _, err := d.SQL().ExecContext(ctx, `UPDATE schema_migrations SET sha256 = 'x' WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(ctx, 0); err == nil || !strings.Contains(err.Error(), "was applied with sha256") {
		t.Fatalf("an edited migration: %v", err)
	}
	e := migrated(t)
	if _, err := e.SQL().ExecContext(ctx, `INSERT INTO schema_migrations VALUES (?, 'future.sql', 'x', 'now')`, Latest()+1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Migrate(ctx, 0); err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("a newer database: %v", err)
	}
	f := migrated(t)
	if _, err := f.Migrate(ctx, 1); err == nil {
		t.Fatal("migrated backwards")
	}
}

func TestAuditIsAppendOnly(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	for i, a := range []string{"POST /api/v1/login", "POST /api/v1/hosts", "DELETE /api/v1/hosts/{id}"} {
		if _, err := d.AppendAudit(ctx, AuditEntry{Username: "admin", Role: RoleAdmin, Action: a, Target: "x", Outcome: 200 + i}); err != nil {
			t.Fatal(err)
		}
	}
	if c, err := d.VerifyAudit(ctx); err != nil || !c.Intact || c.Entries != 3 {
		t.Fatalf("fresh chain %+v %v", c, err)
	}
	for _, q := range []string{`UPDATE audit_log SET outcome = 500 WHERE seq = 2`, `DELETE FROM audit_log WHERE seq = 3`, `DELETE FROM audit_log`} {
		if _, err := d.SQL().ExecContext(ctx, q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := d.CreateJob(ctx, "system", "backup", nil, "schedule"); err != nil { // a state change to protect
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE state_changes SET to_state = 'x'`, `DELETE FROM state_changes`} {
		if _, err := d.SQL().ExecContext(ctx, q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Someone with the database file can drop the trigger; the chain still shows the change.
	if _, err := d.SQL().ExecContext(ctx, `DROP TRIGGER audit_log_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL().ExecContext(ctx, `UPDATE audit_log SET outcome = 500 WHERE seq = 2`); err != nil {
		t.Fatal(err)
	}
	if c, _ := d.VerifyAudit(ctx); c.Intact || c.Broken != 2 {
		t.Fatalf("a changed row: %+v", c)
	}
}

func TestUsersAndSessions(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	d.SetClock(func() time.Time { return now })
	const pw = "correct horse battery staple"
	admin, err := d.CreateUser(ctx, "setup", NewUser{Username: "root1", Role: RoleAdmin, Password: pw, BreakGlass: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		in   NewUser
		want error
	}{
		{NewUser{Username: "short", Role: RoleOperator, Password: "fourteen chars"}, ErrInvalid},
		{NewUser{Username: "listed", Role: RoleOperator, Password: "1qaz2wsx3edc4rfv"}, ErrInvalid},
		{NewUser{Username: "carol", Role: RoleOperator, Password: "carol's own long password"}, ErrInvalid},
		{NewUser{Username: "root1", Role: RoleOperator, Password: "another long password"}, ErrConflict},
		{NewUser{Username: "op", Role: RoleOperator, Password: "another long password", BreakGlass: true}, ErrInvalid},
		{NewUser{Username: "aud", Role: RoleAuditor, Password: "another long password", ExpiresAt: now.Add(31 * 24 * time.Hour).Format(time.RFC3339)}, ErrInvalid},
	} {
		if _, err := d.CreateUser(ctx, "root1", c.in); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.in, err, c.want)
		}
	}
	aud, err := d.CreateUser(ctx, "root1", NewUser{Username: "aud", Role: RoleAuditor, Password: "another long password"})
	if err != nil || aud.ExpiresAt != now.Add(AuditorMaxAge).Format(time.RFC3339Nano) {
		t.Fatalf("auditor %+v %v", aud, err)
	}
	if _, err := d.Authenticate(ctx, "root1", "wrong password!!"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := d.Authenticate(ctx, "nobody", "whatever password"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("missing user: %v", err)
	}
	u, err := d.Authenticate(ctx, "root1", pw)
	if err != nil {
		t.Fatal(err)
	}
	lim := SessionLimits{Idle: 15 * time.Minute}
	sess := func(id int64, addr, dev string) (string, Session) {
		t.Helper()
		tok, s, err := d.CreateSession(ctx, NewSession{UserID: id, Kind: "browser", Method: "webauthn", RemoteAddr: addr, DeviceID: dev, Absolute: 8 * time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return tok, s
	}
	token, s1 := sess(u.ID, "10.0.0.1", "dev-a")
	if s1.NewDevice {
		t.Fatal("a first sign-in is new_device")
	}
	if got, _, err := d.UseSession(ctx, token, lim, false); err != nil || got.ID != admin.ID {
		t.Fatalf("session: %+v %v", got, err)
	}
	// Idle: background requests do not count as activity.
	now = now.Add(10 * time.Minute)
	if _, _, err := d.UseSession(ctx, token, lim, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	var ended *SessionEnded
	if _, _, err := d.UseSession(ctx, token, lim, true); !errors.As(err, &ended) || ended.Reason != EndIdle {
		t.Fatalf("16 minutes with only background requests: %v", err)
	}
	// User activity keeps it; the absolute limit ends it anyway.
	token, _ = sess(u.ID, "10.0.0.1", "dev-a")
	for i := 0; i < 40; i++ {
		now = now.Add(12 * time.Minute)
		if _, _, err := d.UseSession(ctx, token, lim, false); err != nil {
			if !errors.As(err, &ended) || ended.Reason != EndAbsolute || i != 39 {
				t.Fatalf("after %d active requests: %v", i, err)
			}
		}
	}
	// A fourth session ends the oldest; a new address is new_device.
	var toks []string
	for i := 0; i < 4; i++ {
		tk, s := sess(u.ID, "10.0.0.1", "dev-a")
		if s.NewDevice {
			t.Fatal("a known device and address is new_device")
		}
		toks = append(toks, tk)
	}
	if _, _, err := d.UseSession(ctx, toks[0], lim, false); !errors.As(err, &ended) || ended.Reason != EndReplaced {
		t.Fatalf("the oldest of four: %v", err)
	}
	if live, _ := d.Sessions(ctx, u.ID); len(live) != MaxSessionsPerUser {
		t.Fatalf("%d live sessions", len(live))
	}
	if _, s := sess(u.ID, "10.9.9.9", "dev-a"); !s.NewDevice {
		t.Fatal("a new address is not new_device")
	}
	now = now.Add(31 * 24 * time.Hour)
	if _, s := sess(u.ID, "10.0.0.1", "dev-a"); !s.NewDevice {
		t.Fatal("a device not seen for 31 days is not new_device")
	}

	// The last admin, and the last break-glass admin, stay.
	disabled, op, no := true, RoleOperator, false
	for _, p := range []UserPatch{{Disabled: &disabled}, {Role: &op}, {BreakGlass: &no}} {
		if _, err := d.UpdateUser(ctx, "root1", admin.ID, p); !errors.Is(err, ErrConflict) {
			t.Fatalf("%+v of the last admin: %v", p, err)
		}
	}
	second, err := d.CreateUser(ctx, "root1", NewUser{Username: "second", Role: RoleAdmin, Password: pw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateUser(ctx, "second", admin.ID, UserPatch{Disabled: &disabled}); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabling the last break-glass admin while another admin exists: %v", err)
	}
	yes := true
	if _, err := d.UpdateUser(ctx, "root1", second.ID, UserPatch{BreakGlass: &yes}); err != nil {
		t.Fatal(err)
	}
	tk, _ := sess(admin.ID, "10.0.0.1", "dev-a")
	if _, err := d.UpdateUser(ctx, "second", admin.ID, UserPatch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.UseSession(ctx, tk, lim, false); !errors.As(err, &ended) || ended.Reason != EndDisabled {
		t.Fatalf("a disabled user's session: %v", err)
	}
	if _, err := d.Authenticate(ctx, "root1", pw); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("a disabled user logs in: %v", err)
	}
	changes, err := d.StateChanges(ctx, "user", admin.ID, 0, 100)
	if err != nil || len(changes) != 2 || changes[1].To != "disabled" || changes[1].Actor != "second" {
		t.Fatalf("state changes %+v %v", changes, err)
	}

	// Password history: not one of the last 5; an admin's reset makes the user change it.
	if _, err := d.ChangeOwnPassword(ctx, second.ID, pw, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the current password again: %v", err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := d.ChangeOwnPassword(ctx, second.ID, fmt.Sprintf("the %dth long password here", i), 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ChangeOwnPassword(ctx, second.ID, pw, 0); err != nil {
		t.Fatalf("a password 6 changes ago: %v", err)
	}
	tr, _ := sess(aud.ID, "10.0.0.2", "dev-b")
	reset := "an admin chose this one"
	if got, err := d.UpdateUser(ctx, "root1", aud.ID, UserPatch{Password: &reset}); err != nil || got.PasswordChange != PasswordChangeAdminReset {
		t.Fatalf("reset %+v %v", got, err)
	}
	if _, _, err := d.UseSession(ctx, tr, lim, false); !errors.As(err, &ended) || ended.Reason != EndPasswordChange {
		t.Fatalf("after an admin's reset: %v", err)
	}

	// The auditor's account expires; its session ends.
	ta, _ := sess(aud.ID, "10.0.0.2", "dev-b")
	now = now.Add(AuditorMaxAge)
	if _, _, err := d.UseSession(ctx, ta, lim, false); !errors.As(err, &ended) || ended.Reason != EndExpired {
		t.Fatalf("an expired auditor: %v", err)
	}
	// Every session end is in the audit log, with its reason.
	es, _ := d.Audit(ctx, 0, 1000)
	reasons := map[string]bool{}
	for _, e := range es {
		if e.Action == "session_end" {
			reasons[strings.TrimPrefix(strings.SplitN(e.Detail, " (", 2)[0], "ended: ")] = true
		}
	}
	for _, r := range []string{EndIdle, EndAbsolute, EndReplaced, EndDisabled, EndExpired, EndPasswordChange} {
		if !reasons[r] {
			t.Errorf("no session_end %s in the audit log: %v", r, reasons)
		}
	}
	if c, err := d.VerifyAudit(ctx); err != nil || !c.Intact {
		t.Fatalf("audit chain: %+v %v", c, err)
	}
}

// ADR-008 §7: failures are counted per username and per address within 15 minutes; the 10th pauses the account.
func TestSigninFailures(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	d.SetClock(func() time.Time { return now })
	u, err := d.CreateUser(ctx, "setup", NewUser{Username: "op", Role: RoleOperator, Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		paused, until, err := d.SigninFailed(ctx, "op", fmt.Sprintf("10.0.0.%d", i%2))
		if err != nil || paused != (i == 10) || (paused && !until.Equal(now.Add(PauseFor))) {
			t.Fatalf("failure %d: %v %v %v", i, paused, until, err)
		}
		now = now.Add(time.Minute)
	}
	f, _ := d.SigninFailures(ctx, "op", "10.0.0.1")
	if f.ByUser != 10 || f.ByAddr != 5 {
		t.Fatalf("%+v", f)
	}
	got, _ := d.User(ctx, u.ID)
	if locked, _ := got.Locked(now); !locked {
		t.Fatal("not paused")
	}
	if paused, _, _ := d.SigninFailed(ctx, "nobody", "10.0.0.3"); paused {
		t.Fatal("one failure paused")
	}
	if _, err := d.UnlockUser(ctx, "admin", u.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := d.SigninFailures(ctx, "op", "10.0.0.1"); f.ByUser != 0 {
		t.Fatalf("after unlock %+v", f)
	}
	now = now.Add(FailureWindow)
	if f, _ := d.SigninFailures(ctx, "nobody", "10.0.0.3"); f.ByUser != 0 || f.ByAddr != 0 {
		t.Fatalf("outside the window %+v", f)
	}
}

func TestAPITokens(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	d.SetClock(func() time.Time { return now })
	if _, _, err := d.CreateAPIToken(ctx, "root1", NewAPIToken{Name: "ci", Role: RoleAdmin}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an admin token: %v", err)
	}
	if _, _, err := d.CreateAPIToken(ctx, "root1", NewAPIToken{Name: "ci", Role: RoleOperator, ExpiresAt: now.Add(91 * 24 * time.Hour).Format(time.RFC3339)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("91 days: %v", err)
	}
	tok, at, err := d.CreateAPIToken(ctx, "root1", NewAPIToken{Name: "ci", Role: RoleOperator, AllowedAddrs: []string{"10.1.0.0/16", "192.0.2.7"}})
	if err != nil || at.ExpiresAt != now.Add(TokenMaxAge).Format(time.RFC3339Nano) {
		t.Fatalf("%+v %v", at, err)
	}
	if _, _, err := d.UseAPIToken(ctx, tok, "10.2.0.1"); !errors.Is(err, ErrTokenRefused) {
		t.Fatalf("from another address: %v", err)
	}
	if _, use, err := d.UseAPIToken(ctx, tok, "10.1.3.4"); err != nil || !use.First {
		t.Fatalf("first use %+v %v", use, err)
	}
	d.UseAPIToken(ctx, tok, "192.0.2.7")
	now = now.Add(24 * time.Hour)
	if _, use, err := d.UseAPIToken(ctx, tok, "192.0.2.7"); err != nil || use.First || use.ClosedDay != "2026-11-16" || use.ClosedCount != 2 {
		t.Fatalf("next day %+v %v", use, err)
	}
	if _, _, err := d.UseAPIToken(ctx, "gaiat_nothing", "192.0.2.7"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unknown token: %v", err)
	}
	if _, err := d.RevokeAPIToken(ctx, "root1", at.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.UseAPIToken(ctx, tok, "192.0.2.7"); !errors.Is(err, ErrTokenRefused) {
		t.Fatalf("revoked: %v", err)
	}
	tok2, _, _ := d.CreateAPIToken(ctx, "root1", NewAPIToken{Name: "ci2", Role: RoleAuditor})
	now = now.Add(TokenMaxAge)
	if _, _, err := d.UseAPIToken(ctx, tok2, "192.0.2.7"); !errors.Is(err, ErrTokenRefused) {
		t.Fatalf("expired: %v", err)
	}
}

func TestJobStates(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	j, err := d.CreateJob(ctx, "system", "backup", nil, "schedule")
	if err != nil || j.State != "queued" {
		t.Fatalf("%+v %v", j, err)
	}
	if _, err := d.SetJobState(ctx, "system", j.ID, "ok", "done", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("queued -> ok: %v", err)
	}
	if _, err := d.SetJobState(ctx, "system", j.ID, "running", "worker", ""); err != nil {
		t.Fatal(err)
	}
	if j, err = d.SetJobState(ctx, "system", j.ID, "failed", "worker", "checkpoint missing"); err != nil || j.Error != "checkpoint missing" || j.FinishedAt == "" {
		t.Fatalf("%+v %v", j, err)
	}
	if _, err := d.SetJobState(ctx, "system", j.ID, "running", "again", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("failed -> running: %v", err)
	}
	cs, _ := d.StateChanges(ctx, "job", j.ID, 0, 100)
	var path []string
	for _, c := range cs {
		path = append(path, c.From+">"+c.To)
	}
	if !slices.Equal(path, []string{">queued", "queued>running", "running>failed"}) {
		t.Fatalf("job history %v", path)
	}
	// The entity alone (no entity_id) lists every record of it, not none.
	if all, err := d.StateChanges(ctx, "job", 0, 0, 100); err != nil || len(all) != len(cs) {
		t.Fatalf("every job: %d changes, want %d: %v", len(all), len(cs), err)
	}
}

func TestDomainConstraints(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	h, err := d.CreateHost(ctx, HostInput{Name: "kvm1", Address: "kvm1.lab", SSHUser: "gaia"})
	if err != nil || h.SSHPort != 22 {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := d.CreateHost(ctx, HostInput{Name: "bad name", Address: "x", SSHUser: "gaia"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad name: %v", err)
	}
	v, err := d.CreateVM(ctx, "admin", VMInput{HostID: h.ID, Name: "web1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateVM(ctx, "admin", VMInput{HostID: 999, Name: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a VM on a missing host: %v", err)
	}
	if err := d.DeleteHost(ctx, h.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleting a host with VMs: %v", err)
	}
	if _, err := d.CreateDisk(ctx, v.ID, DiskInput{Target: "vda", SizeBytes: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	r, err := d.CreateRepository(ctx, Repository{Name: "main", Kind: "filesystem", Location: "/srv/repo", Splitter: "FIXED-1M", Hash: "BLAKE2B-256-128"})
	if err != nil {
		t.Fatal(err)
	}
	in := PolicyInput{Name: "gold", RepositoryID: r.ID, BackupIntervalS: 900, TestIntervalS: 86400, KeepLast: 8, RPOTargetS: 3600,
		RTOTargetS: 1800, MaxProofAgeS: 3600, CheckTemplate: "linux-basic"}
	if _, err := d.CreatePolicy(ctx, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a proof age shorter than the test interval: %v", err)
	}
	in.MaxProofAgeS = 2 * 86400
	p, err := d.CreatePolicy(ctx, in)
	if err != nil || string(p.Checks) != "[]" {
		t.Fatalf("%+v %v", p, err)
	}
	if v, err = d.UpdateVM(ctx, v.ID, VMPatch{PolicyID: &p.ID}); err != nil || v.PolicyID == nil || *v.PolicyID != p.ID {
		t.Fatalf("%+v %v", v, err)
	}
	if err := d.DeletePolicy(ctx, p.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleting a policy in use: %v", err)
	}
	zero := int64(0)
	if v, err = d.UpdateVM(ctx, v.ID, VMPatch{PolicyID: &zero}); err != nil || v.PolicyID != nil {
		t.Fatalf("%+v %v", v, err)
	}
	if err := d.DeletePolicy(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteVM(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if ks, _ := d.Disks(ctx, v.ID); len(ks) != 0 {
		t.Fatalf("disks of a deleted VM: %+v", ks)
	}
}

func TestBackupPointsDisksVerifications(t *testing.T) {
	ctx := context.Background()
	d := migrated(t)
	h, _ := d.CreateHost(ctx, HostInput{Name: "kvm1", Address: "kvm1.lab", SSHUser: "gaia-backup"})
	if h, err := d.PinHostKey(ctx, "admin", h.ID, "ssh-ed25519 AAAA", "SHA256:x"); err != nil || h.SSHHostKey != "ssh-ed25519 AAAA" {
		t.Fatalf("pin %+v %v", h, err)
	}
	v, _ := d.CreateVM(ctx, "admin", VMInput{HostID: h.ID, Name: "web1"})
	r, _ := d.CreateRepository(ctx, Repository{Name: "main", Kind: "filesystem", Location: "/r", Splitter: "FIXED-1M", Hash: "BLAKE2B-256-128"})

	if err := d.SyncDisks(ctx, "system", v.ID, []DiskInput{{"vda", 100}, {"vdb", 200}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncDisks(ctx, "system", v.ID, []DiskInput{{"vda", 100}, {"vdb", 300}, {"vdc", 50}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncDisks(ctx, "system", v.ID, []DiskInput{{"vda", 100}, {"vdb", 300}}); err != nil {
		t.Fatal(err)
	}
	ks, _ := d.Disks(ctx, v.ID)
	if len(ks) != 2 || ks[1].SizeBytes != 300 {
		t.Fatalf("disks %+v", ks)
	}
	all, _ := d.StateChanges(ctx, "", 0, 0, 1000)
	var diskEvents []string
	for _, c := range all {
		if c.Entity == "disk" {
			diskEvents = append(diskEvents, c.From+">"+c.To)
		}
	}
	if !slices.Equal(diskEvents, []string{">100", ">200", "200>300", ">50", "50>"}) {
		t.Fatalf("disk history %v", diskEvents)
	}

	if _, err := d.LatestPoint(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no point yet: %v", err)
	}
	j, _ := d.CreateJob(ctx, "system", "backup", &v.ID, "schedule")
	if _, ok, _ := d.OpenJob(ctx, v.ID, "backup"); !ok {
		t.Fatal("open job not found")
	}
	if err := d.SetJobCheckpoint(ctx, "system", j.ID, "gaia-1"); err != nil {
		t.Fatal(err)
	}
	p, err := d.CreateBackupPoint(ctx, "system", Point{VMID: v.ID, JobID: j.ID, RepositoryID: r.ID, Kind: "full", TakenAt: "2026-11-16T12:00:00Z",
		Checkpoint: "gaia-1", Reason: "first backup"}, []PointDisk{{Target: "vda", SizeBytes: 100, BlockSize: 1 << 20, StoreID: "m1", WrittenBytes: 10},
		{Target: "vdb", SizeBytes: 300, BlockSize: 1 << 20, StoreID: "m2", WrittenBytes: 20}})
	if err != nil || p.Bytes != 30 || p.State != "present" || p.CheckState != "unchecked" {
		t.Fatalf("point %+v %v", p, err)
	}
	if _, err := d.LatestPoint(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("an unchecked point became a parent")
	}
	if _, err := d.SetPointCheck(ctx, "system", p.ID, true, "post-backup check"); err != nil {
		t.Fatal(err)
	}
	if lp, err := d.LatestPoint(ctx, v.ID); err != nil || lp.ID != p.ID {
		t.Fatalf("latest %+v %v", lp, err)
	}
	if pd, _ := d.PointDisks(ctx, p.ID); len(pd) != 2 || pd[1].StoreID != "m2" {
		t.Fatalf("point disks %+v", pd)
	}
	if err := d.PrunePoint(ctx, "system", p.ID, "keep_last"); err != nil {
		t.Fatal(err)
	}
	if err := d.PrunePoint(ctx, "system", p.ID, "again"); !errors.Is(err, ErrConflict) {
		t.Fatalf("pruned twice: %v", err)
	}
	if _, err := d.LatestPoint(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a pruned point became a parent")
	}
	if ji, err := d.JobInfo(ctx, j.ID); err != nil || ji.Checkpoint != "gaia-1" {
		t.Fatalf("job info %+v %v", ji, err)
	}

	ver, err := d.StartVerification(ctx, "system", r.ID, "full", "requested")
	if err != nil {
		t.Fatal(err)
	}
	ver.State, ver.PointsChecked, ver.Affected = "critical", 3, []byte(`[{"point_id":1}]`)
	if ver, err = d.FinishVerification(ctx, "system", ver); err != nil || ver.State != "critical" || ver.FinishedAt == "" {
		t.Fatalf("verification %+v %v", ver, err)
	}
	if last, ok, _ := d.LastVerification(ctx, r.ID, "full"); !ok || last.ID != ver.ID {
		t.Fatalf("last verification %+v", last)
	}
	if js, err := d.FailInterruptedJobs(ctx, "server restarted"); err != nil || len(js) != 0 {
		t.Fatalf("interrupted %v %v", js, err)
	}
}
