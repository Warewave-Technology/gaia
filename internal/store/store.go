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
func Migrate(ctx context.Context, dsn string) (err error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, sqlDB.Close()) }()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return err
	}
	return migrateRiver(ctx, dsn)
}

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
}

// Open connects and checks the connection.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the connections.
func (s *Store) Close() { s.pool.Close() }

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
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtextextended('gaia/contract/' || $1, 0))`, contract).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s: %w", contract, ErrLocked)
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
