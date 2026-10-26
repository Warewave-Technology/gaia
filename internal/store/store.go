package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for goose
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/Warewave-Technology/gaia/internal/store/db"
)

//go:generate sqlc generate

//go:embed migrations/*.sql
var migrations embed.FS

var (
	// ErrLocked: another run holds the contract.
	ErrLocked = errors.New("contract is locked by another run")
	// ErrNotFound: no such row.
	ErrNotFound = errors.New("not found")
)

// NameTakenError: a live host already holds the hostname.
type NameTakenError struct{ Name string }

func (e *NameTakenError) Error() string {
	return fmt.Sprintf("hostname %s is held by another host", e.Name)
}

// Migrate brings the schema up to date. It needs a role that owns the
// schema (gaia_owner, design 12.2).
func Migrate(ctx context.Context, dsn string) error {
	_, err := MigrateCount(ctx, dsn)
	return err
}

// MigrateCount is Migrate, and says how many of Gaia's migrations it
// applied.
func MigrateCount(ctx context.Context, dsn string) (n int, err error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, sqlDB.Close()) }()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return 0, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		return 0, err
	}
	done, err := p.Up(ctx)
	if err != nil {
		return 0, err
	}
	if err := migrateRiver(ctx, dsn); err != nil {
		return 0, err
	}
	_, err = sqlDB.ExecContext(ctx, appGrants)
	return len(done), err
}

// AppRole is the role Gaia runs as (design 12.2). The database
// administrator creates it; gaia migrate, run as the owner, grants it
// what it needs after every migration. It may not change or delete run
// records and approvals; of a record it may only fill in the archive
// location, once (the trigger checks that).
const AppRole = "gaia_app"

const appGrants = `
do $$
begin
  if not exists (select 1 from pg_roles where rolname = 'gaia_app') then
    return;
  end if;
  grant usage on schema public to gaia_app;
  grant select, insert, update, delete on all tables in schema public to gaia_app;
  grant usage, select on all sequences in schema public to gaia_app;
  revoke update, delete, truncate on run_records from gaia_app;
  grant update (archived) on run_records to gaia_app;
  revoke update, delete, truncate on approvals from gaia_app;
  revoke update, delete, truncate on settings_versions, settings_objects, audit_log from gaia_app;
  revoke all on goose_db_version from gaia_app;
  grant select on goose_db_version to gaia_app;
end
$$;`

// migrateRiver creates and updates the job queue's tables (River).
func migrateRiver(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

// Store is Gaia's database.
type Store struct {
	pool *pgxpool.Pool
	// locks holds the transactions of advisory locks. A lock's
	// transaction stays open while its work runs, and the work takes
	// connections of its own: from one pool, a few locks held at once
	// would use up the connections their work waits for.
	locks *pgxpool.Pool
}

// Pool sizes when the DSN does not set pool_max_conns: pgx's default
// (the CPU count, at least 4) is too small for Gaia's background loops.
const (
	defaultConns = 20
	lockConns    = 32
)

// Open connects and checks the connection.
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = defaultConns
	}
	lcfg := cfg.Copy()
	lcfg.MaxConns, lcfg.MinConns = lockConns, 0
	lcfg.MaxConnIdleTime = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	locks, err := pgxpool.NewWithConfig(ctx, lcfg)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, locks: locks}, nil
}

// Close releases the connections.
func (s *Store) Close() {
	s.locks.Close()
	s.pool.Close()
}

// Q returns the queries, outside any transaction.
func (s *Store) Q() *db.Queries { return db.New(s.pool) }

// Tx runs fn in a transaction; it commits when fn returns nil.
func (s *Store) Tx(ctx context.Context, fn func(q *db.Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return fn(db.New(tx)) })
}

// NewHost is a host about to be created.
type NewHost struct {
	UID, Contract, Hostname, Site, Network string
	Seq                                    int
	Run                                    string
}

// CreateHost records a host in provisioning and its hostname lease in one
// transaction. A hostname held by another live host is a *NameTakenError.
func (s *Store) CreateHost(ctx context.Context, h NewHost) error {
	err := s.Tx(ctx, func(q *db.Queries) error {
		if err := q.InsertHost(ctx, db.InsertHostParams{
			Uid: h.UID, Contract: h.Contract, Hostname: h.Hostname, Site: h.Site, Network: h.Network,
			Seq: int32(h.Seq), Status: db.HostStatusProvisioning, CreatedRun: text(h.Run),
		}); err != nil {
			return err
		}
		return q.HoldName(ctx, db.HoldNameParams{Name: h.Hostname, Uid: h.UID})
	})
	if isUnique(err, "name_leases_held") {
		return &NameTakenError{Name: h.Hostname}
	}
	return err
}

// WithContractLock runs fn while holding the contract's lock, so that two
// runs never change one contract at once (design 12.4). The lock is a
// transaction-level advisory lock: it is released when fn returns, also
// through a connection pooler in transaction mode.
func (s *Store) WithContractLock(ctx context.Context, contract string, fn func(context.Context) error) error {
	return s.WithLock(ctx, "gaia/contract/"+contract, fn)
}

// WithLock runs fn while holding the named advisory lock, or returns
// ErrLocked at once when someone else holds it.
func (s *Store) WithLock(ctx context.Context, name string, fn func(context.Context) error) error {
	return pgx.BeginFunc(ctx, s.locks, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtextextended($1, 0))`, name).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s: %w", strings.TrimPrefix(name, "gaia/contract/"), ErrLocked)
		}
		return fn(ctx)
	})
}

// WaitLock runs fn holding the advisory lock name, waiting for it when
// another replica holds it.
func (s *Store) WaitLock(ctx context.Context, name string, fn func(context.Context) error) error {
	return pgx.BeginFunc(ctx, s.locks, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, name); err != nil {
			return err
		}
		return fn(ctx)
	})
}

// NewUID returns a new host UID: 16 lowercase base32 characters.
func NewUID() string { return newID(10) }

// NewRunID returns a new run ID.
func NewRunID() string { return "run-" + newID(10) }

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(b32.EncodeToString(b))
}

func isUnique(err error, constraint string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == constraint
}

// IsNotFound reports whether err means no row.
func IsNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound)
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// DeleteHost marks a decommissioned host deleted, releases its hostname
// and forgets its external IDs, in one transaction.
func (s *Store) DeleteHost(ctx context.Context, uid string) error {
	return s.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetHostStatus(ctx, db.SetHostStatusParams{Uid: uid, Status: db.HostStatusDeleted}); err != nil {
			return err
		}
		if err := q.ReleaseNames(ctx, uid); err != nil {
			return err
		}
		return q.DeleteExternalIDs(ctx, uid)
	})
}

// ReplaceHost gives a server's place to a new one (design 9.5): the old
// host is deleted, with its name and IDs, and the new one takes the same
// name and sequence number, at once. The old IDs are the caller's to keep
// for removing what they name.
func (s *Store) ReplaceHost(ctx context.Context, old, uid, run string) error {
	return s.Tx(ctx, func(q *db.Queries) error {
		h, err := q.Host(ctx, old)
		if err != nil {
			return err
		}
		if err := q.SetHostStatus(ctx, db.SetHostStatusParams{Uid: old, Status: db.HostStatusDeleted}); err != nil {
			return err
		}
		if err := q.ReleaseNames(ctx, old); err != nil {
			return err
		}
		if err := q.DeleteExternalIDs(ctx, old); err != nil {
			return err
		}
		if err := q.InsertHost(ctx, db.InsertHostParams{
			Uid: uid, Contract: h.Contract, Hostname: h.Hostname, Site: h.Site, Network: h.Network,
			Seq: h.Seq, Status: db.HostStatusProvisioning, CreatedRun: text(run),
		}); err != nil {
			return err
		}
		return q.HoldName(ctx, db.HoldNameParams{Name: h.Hostname, Uid: uid})
	})
}

// Pool is the connection pool, for the job queue.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ContractBusy reports whether a run holds the contract's lock right now.
// It is a hint for the queue: a run that finds the lock taken anyway
// fails with ErrLocked.
func (s *Store) ContractBusy(ctx context.Context, contract string) (bool, error) {
	busy := false
	err := s.WithContractLock(ctx, contract, func(context.Context) error { return nil })
	if errors.Is(err, ErrLocked) {
		busy, err = true, nil
	}
	return busy, err
}

// TxFunc runs fn in a transaction, for work that spans Gaia's tables and
// the job queue's.
func (s *Store) TxFunc(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, fn)
}

// NewUserID returns a new person's ID.
func NewUserID() string { return "usr-" + newID(10) }

// NewSessionID returns a new session's ID.
func NewSessionID() string { return "ses-" + newID(10) }

// NewChangeID returns a new change's ID.
func NewChangeID() string { return "chg-" + newID(10) }

// NewProposalID returns a new settings proposal's ID.
func NewProposalID() string { return "set-" + newID(10) }

// NewFlowID returns a new sign-in ceremony's ID.
func NewFlowID() string { return "flw-" + newID(16) }
