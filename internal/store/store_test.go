package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

func open(t *testing.T) *Store {
	t.Helper()
	itest.Require(t)
	dsn := itest.StartPostgres(t)
	ctx := context.Background()
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Migrating twice is a no-op.
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestIDs(t *testing.T) {
	re := regexp.MustCompile(`^[a-z2-7]{16}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		u := NewUID()
		if !re.MatchString(u) || seen[u] {
			t.Fatalf("NewUID = %q (seen %v)", u, seen[u])
		}
		seen[u] = true
	}
	if r := NewRunID(); !regexp.MustCompile(`^run-[a-z2-7]{16}$`).MatchString(r) {
		t.Errorf("NewRunID = %q", r)
	}
}

func TestHostsAndNames(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	q := s.Q()

	a := NewHost{UID: NewUID(), Contract: "poc-web", Hostname: "labtpoc01", Site: "lab", Seq: 1, Run: "run-1"}
	if err := s.CreateHost(ctx, a); err != nil {
		t.Fatal(err)
	}
	// Another host may not hold the same name while a holds it...
	b := NewHost{UID: NewUID(), Contract: "poc-web2", Hostname: "labtpoc01", Site: "lab", Seq: 1}
	var taken *NameTakenError
	if err := s.CreateHost(ctx, b); !errors.As(err, &taken) || taken.Name != "labtpoc01" {
		t.Fatalf("second holder: %v", err)
	}
	// ...and the failed create left nothing behind.
	if _, err := q.Host(ctx, b.UID); !IsNotFound(err) {
		t.Errorf("host b exists after a failed create: %v", err)
	}
	// The same seq in the same contract and site is taken too.
	c := NewHost{UID: NewUID(), Contract: "poc-web", Hostname: "labtpoc99", Site: "lab", Seq: 1}
	if err := s.CreateHost(ctx, c); err == nil {
		t.Error("duplicate seq accepted")
	}

	live, err := q.LiveHosts(ctx, "poc-web")
	if err != nil || len(live) != 1 || live[0].Uid != a.UID || live[0].Status != db.HostStatusProvisioning {
		t.Fatalf("LiveHosts = %+v, %v", live, err)
	}
	if holder, err := q.NameHolder(ctx, "labtpoc01"); err != nil || holder != a.UID {
		t.Errorf("NameHolder = %q, %v", holder, err)
	}

	// External IDs are upserted and found back by value.
	for _, id := range []string{"9401", "9402"} {
		if err := q.SetExternalID(ctx, db.SetExternalIDParams{Uid: a.UID, Provider: "pve-lab", Kind: "vm", ExternalID: id}); err != nil {
			t.Fatal(err)
		}
	}
	ids, _ := q.ExternalIDs(ctx, a.UID)
	if len(ids) != 1 || ids[0].ExternalID != "9402" {
		t.Errorf("ExternalIDs = %+v", ids)
	}
	if uid, err := q.HostByExternalID(ctx, db.HostByExternalIDParams{Provider: "pve-lab", Kind: "vm", ExternalID: "9402"}); err != nil || uid != a.UID {
		t.Errorf("HostByExternalID = %q, %v", uid, err)
	}

	// Deleting the host frees its name, seq and external IDs.
	if err := s.DeleteHost(ctx, a.UID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := q.ExternalIDs(ctx, a.UID); len(ids) != 0 {
		t.Errorf("external IDs after delete: %+v", ids)
	}

	if err := s.CreateHost(ctx, b); err != nil {
		t.Errorf("name not reusable after release: %v", err)
	}
	if err := q.SetExternalID(ctx, db.SetExternalIDParams{Uid: b.UID, Provider: "pve-lab", Kind: "vm", ExternalID: "9402"}); err != nil {
		t.Errorf("VM ID not reusable after delete: %v", err)
	}
	if err := s.CreateHost(ctx, NewHost{UID: NewUID(), Contract: "poc-web", Hostname: "labtpoc02", Site: "lab", Seq: 1}); err != nil {
		t.Errorf("seq not reusable after delete: %v", err)
	}
	gone, _ := q.Host(ctx, a.UID)
	if gone.Status != db.HostStatusDeleted || !gone.DeletedAt.Valid {
		t.Errorf("deleted host = %+v", gone)
	}
}

func TestContractLock(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	inside := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error)
	go func() {
		done <- s.WithContractLock(ctx, "poc-web", func(context.Context) error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside
	if err := s.WithContractLock(ctx, "poc-web", func(context.Context) error { return nil }); !errors.Is(err, ErrLocked) {
		t.Errorf("second lock on poc-web: %v", err)
	}
	if err := s.WithContractLock(ctx, "other", func(context.Context) error { return nil }); err != nil {
		t.Errorf("lock on another contract: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Released when the first run ended.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := s.WithContractLock(ctx, "poc-web", func(context.Context) error { return nil })
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock not released: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Lock holders keep a transaction open while their work runs, and the
// work needs connections of its own: holding locks must never use up the
// connections the work waits for (the lab hung that way with four).
func TestLocksDoNotStarveThePool(t *testing.T) {
	itest.Require(t)
	dsn := itest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	s, err := Open(ctx, dsn+sep+"pool_max_conns=2")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const n = 4
	var inside sync.WaitGroup
	inside.Add(n)
	all := make(chan struct{})
	go func() { inside.Wait(); close(all) }()
	errs := make(chan error, n)
	for i := range n {
		go func() {
			errs <- s.WithLock(ctx, fmt.Sprintf("test/%d", i), func(ctx context.Context) error {
				inside.Done()
				select { // every lock is held at once
				case <-all:
				case <-ctx.Done():
					return fmt.Errorf("not every lock could be taken: %w", ctx.Err())
				}
				_, err := s.Q().DirectoryUsers(ctx, "none")
				return err
			})
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuns(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	q := s.Q()
	id := NewRunID()
	if err := q.StartRun(ctx, db.StartRunParams{ID: id, Contracts: []string{"poc-web"}, Commit: "c0ffee", PlanHash: "sha256:x"}); err != nil {
		t.Fatal(err)
	}
	if err := q.FinishRun(ctx, db.FinishRunParams{ID: id, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	r, err := q.Run(ctx, id)
	if err != nil || r.Status != "succeeded" || !r.FinishedAt.Valid || r.Contracts[0] != "poc-web" {
		t.Errorf("Run = %+v, %v", r, err)
	}
}

func TestAppRoleCannotRewriteRecords(t *testing.T) {
	itest.Require(t)
	ctx := context.Background()
	dsn := itest.StartPostgres(t)
	owner, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(ctx) }()
	if _, err := owner.Exec(ctx, "create role gaia_app login password 'app'"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	appDSN := strings.Replace(dsn, "gaia_owner:itest", "gaia_app:app", 1)
	s, err := Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := s.Q()
	if err := q.StartRun(ctx, db.StartRunParams{ID: "run-1", Contracts: []string{"c"}, Commit: "x", PlanHash: "h"}); err != nil {
		t.Fatalf("the app role cannot write runs: %v", err)
	}
	if _, err := q.InsertRunRecord(ctx, db.InsertRunRecordParams{RunID: "run-1", Body: "{}", PrevHash: "", RecordHash: "h1"}); err != nil {
		t.Fatalf("the app role cannot insert records: %v", err)
	}
	if err := q.SetRunRecordArchived(ctx, db.SetRunRecordArchivedParams{RunID: "run-1", Archived: pgtype.Text{String: "s3://b/k", Valid: true}}); err != nil {
		t.Errorf("the app role cannot note the archive: %v", err)
	}
	if _, err := q.InsertAudit(ctx, db.InsertAuditParams{Actor: "ayse", Action: "settings.write", Result: "ok"}); err != nil {
		t.Errorf("the app role cannot write the audit log: %v", err)
	}
	if err := q.InsertSettingsVersion(ctx, db.InsertSettingsVersionParams{Version: 1, Author: "gaia", Reason: "r", ContentHash: "h"}); err != nil {
		t.Errorf("the app role cannot write a settings version: %v", err)
	}
	for _, sql := range []string{
		"update run_records set body = '{\"x\":1}'",
		"delete from run_records",
		"truncate run_records",
		"update audit_log set actor = 'x'",
		"delete from audit_log",
		"truncate audit_log",
		"update settings_versions set author = 'x'",
		"delete from settings_objects",
		"alter table run_records disable trigger user",
		"drop table hosts",
	} {
		_, err := s.Pool().Exec(ctx, sql)
		if err == nil || !strings.Contains(err.Error(), "permission denied") && !strings.Contains(err.Error(), "must be owner") {
			t.Errorf("%s: %v", sql, err)
		}
	}
}
