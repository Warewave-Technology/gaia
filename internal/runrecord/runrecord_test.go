package runrecord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

func setup(t *testing.T) (*store.Store, string) {
	t.Helper()
	itest.Require(t)
	ctx := context.Background()
	dsn := itest.StartPostgres(t)
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dsn
}

func run(t *testing.T, s *store.Store, id string) Record {
	t.Helper()
	ctx := context.Background()
	if err := s.Q().StartRun(ctx, db.StartRunParams{ID: id, Contracts: []string{"web"}, Commit: "c0ffee", PlanHash: "sha256:p"}); err != nil {
		t.Fatal(err)
	}
	return Record{
		Run: id, RequestedBy: "ops@host", ApprovedBy: []string{}, Source: Source{Repo: "/repo", Commit: "c0ffee"},
		CatalogCommit: "c0ffee", Contracts: []string{"web"}, PlanHash: "sha256:p", ExecutedAs: map[string]string{"linux": "gaia"},
		StartedAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), FinishedAt: time.Date(2026, 10, 24, 12, 5, 0, 0, time.UTC),
		Result: "succeeded", Changes: []Change{{Contract: "web", UID: "u1", Hostname: "web01", Stage: "plan", Action: "create"}},
	}
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func TestChain(t *testing.T) {
	s, dsn := setup(t)
	ctx := context.Background()
	var recs []db.RunRecord
	for _, id := range []string{"run-a", "run-b", "run-c"} {
		r, err := Append(ctx, s, run(t, s, id))
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	if recs[0].PrevHash != "" || recs[1].PrevHash != recs[0].RecordHash || recs[2].PrevHash != recs[1].RecordHash {
		t.Fatalf("chain = %+v", recs)
	}
	if recs[0].RecordHash != Hash("", []byte(recs[0].Body)) {
		t.Error("hash does not cover the body")
	}
	var body Record
	if err := json.Unmarshal([]byte(recs[1].Body), &body); err != nil || body.Run != "run-b" || body.Changes[0].Hostname != "web01" {
		t.Errorf("body = %+v, %v", body, err)
	}
	if n, err := Verify(ctx, s.Q()); err != nil || n != 3 {
		t.Fatalf("verify: %d, %v", n, err)
	}

	// The table refuses changes, deletes and truncation.
	c := connect(t, dsn)
	for _, sql := range []string{
		`update run_records set body = '{}' where run_id = 'run-b'`,
		`delete from run_records where run_id = 'run-b'`,
		`truncate run_records`,
	} {
		if _, err := c.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Errorf("%s: %v", sql, err)
		}
	}
	// Only the archive location can be set, once.
	if err := s.Q().SetRunRecordArchived(ctx, db.SetRunRecordArchivedParams{RunID: "run-a", Archived: text("s3://b/k")}); err != nil {
		t.Errorf("archive location: %v", err)
	}
	if err := s.Q().SetRunRecordArchived(ctx, db.SetRunRecordArchivedParams{RunID: "run-a", Archived: text("s3://b/other")}); err == nil {
		t.Error("the archive location was changed")
	}

	// Someone who can bypass the trigger still breaks the chain.
	if _, err := c.Exec(ctx, `alter table run_records disable trigger user`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `update run_records set body = replace(body, 'web01', 'web99') where run_id = 'run-b'`); err != nil {
		t.Fatal(err)
	}
	_, err := Verify(ctx, s.Q())
	var b *Broken
	if !errors.As(err, &b) || b.Run != "run-b" || !strings.Contains(b.Reason, "does not match its hash") {
		t.Errorf("tampered body: %v", err)
	}
	if _, err := c.Exec(ctx, `delete from run_records where run_id = 'run-b'`); err != nil {
		t.Fatal(err)
	}
	n, err := Verify(ctx, s.Q())
	if !errors.As(err, &b) || b.Run != "run-c" || n != 1 || !strings.Contains(b.Reason, "points at") {
		t.Errorf("deleted record: %d, %v", n, err)
	}
}

type memArchive map[string][]byte

func (m memArchive) Put(_ context.Context, key string, data []byte, until time.Time) (string, error) {
	if time.Until(until) < 23*time.Hour {
		return "", errors.New("retention too short")
	}
	m[key] = data
	return "mem://" + key, nil
}

func TestStore(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	r, err := Append(ctx, s, run(t, s, "run-a"))
	if err != nil {
		t.Fatal(err)
	}
	a := memArchive{}
	loc, err := Store(ctx, s, a, r, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key := Key(r)
	if !strings.HasPrefix(key, "records/") || !strings.HasSuffix(key, "/run-a.json") || loc != "mem://"+key {
		t.Errorf("key %s, location %s", key, loc)
	}
	var got Archived
	if err := json.Unmarshal(a[key], &got); err != nil {
		t.Fatal(err)
	}
	// The archived copy can be checked on its own.
	if got.RecordHash != r.RecordHash || Hash(got.PrevHash, got.Body) != got.RecordHash {
		t.Errorf("archived = %+v", got)
	}
	rec, _ := s.Q().RunRecord(ctx, "run-a")
	if rec.Archived.String != loc {
		t.Errorf("archived at %q", rec.Archived.String)
	}
}
