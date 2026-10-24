package store

import (
	"context"
	"errors"
	"regexp"
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
