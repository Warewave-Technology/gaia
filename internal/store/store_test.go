package store

import (
	"context"
	"errors"
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
	if err != nil || !slices.Equal(done, []int{1, 2, 3, 4, 5}) || Latest() != 5 {
		t.Fatalf("from empty: %v %v (latest %d)", done, err, Latest())
	}
	if done, err := d.Migrate(ctx, 0); err != nil || len(done) != 0 {
		t.Fatalf("again: %v %v", done, err)
	}
	applied, err := d.Applied(ctx)
	if err != nil || len(applied) != 5 || applied[4].Name != "0005_recovery.sql" {
		t.Fatalf("applied %+v %v", applied, err)
	}
}

// A database at the previous schema version, with data in it, migrates to the latest: the data is kept and the
// schema ends up exactly as on a database migrated from empty.
func TestMigrationFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	old := open(t)
	if _, err := old.Migrate(ctx, Latest()-1); err != nil {
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
	done, err := old.Migrate(ctx, 0)
	if err != nil || !slices.Equal(done, []int{Latest()}) {
		t.Fatalf("previous -> latest: %v %v", done, err)
	}
	if _, err := old.Authenticate(ctx, "olduser", "previous-schema-password"); err != nil {
		t.Fatalf("a user from the previous schema: %v", err)
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
	admin, err := d.CreateUser(ctx, "setup", "admin", RoleAdmin, "a long admin password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser(ctx, "admin", "short", RoleOperator, "short"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a short password: %v", err)
	}
	if _, err := d.CreateUser(ctx, "admin", "admin", RoleOperator, "another long password"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a duplicate user: %v", err)
	}
	if _, err := d.Authenticate(ctx, "admin", "wrong password!!"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := d.Authenticate(ctx, "nobody", "whatever password"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("missing user: %v", err)
	}
	u, err := d.Authenticate(ctx, "admin", "a long admin password")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := d.CreateSession(ctx, u, "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := d.SessionUser(ctx, token); err != nil || got.ID != admin.ID {
		t.Fatalf("session: %+v %v", got, err)
	}
	disabled := true
	if _, err := d.UpdateUser(ctx, "admin", admin.ID, UserPatch{Disabled: &disabled}); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabling the last admin: %v", err)
	}
	op := RoleOperator
	if _, err := d.UpdateUser(ctx, "admin", admin.ID, UserPatch{Role: &op}); !errors.Is(err, ErrConflict) {
		t.Fatalf("demoting the last admin: %v", err)
	}
	second, err := d.CreateUser(ctx, "admin", "second", RoleAdmin, "second admin password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.UpdateUser(ctx, "second", admin.ID, UserPatch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.SessionUser(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("a disabled user's session: %v", err)
	}
	if _, err := d.Authenticate(ctx, "admin", "a long admin password"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("a disabled user logs in: %v", err)
	}
	t2, _, _ := d.CreateSession(ctx, second, "127.0.0.1", time.Hour)
	now = now.Add(2 * time.Hour)
	if _, _, err := d.SessionUser(ctx, t2); !errors.Is(err, ErrNoSession) {
		t.Fatalf("an expired session: %v", err)
	}
	changes, err := d.StateChanges(ctx, "user", admin.ID, 0, 100)
	if err != nil || len(changes) != 2 || changes[1].To != "disabled" || changes[1].Actor != "second" {
		t.Fatalf("state changes %+v %v", changes, err)
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
	if n, err := d.FailInterruptedJobs(ctx, "server restarted"); err != nil || n != 0 {
		t.Fatalf("interrupted %d %v", n, err)
	}
}
