// Package store keeps Gaia's state in SQLite (ADR-007 §2) through the pure-Go driver modernc.org/sqlite: users
// and sessions, the append-only audit log and state-change log, and the domain (hosts, VMs, disks, repositories,
// policies, jobs, points, restore tests, evidence bundles, state backups). The schema is created and changed only
// by the numbered migrations in migrations/, each applied once, in order, and recorded with its hash.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Errors a caller can act on; everything else is an internal error.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
)

// DB is Gaia's state database.
type DB struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (creating) the database at path. It does not migrate: call Migrate.
func Open(path string) (*DB, error) {
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(10000)", "synchronous(FULL)"} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// One connection: writes are serialised (the audit chain never forks) and SQLite never answers BUSY.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		db.Close()
		return nil, fmt.Errorf("open %s: journal mode %q, want wal (%v)", path, mode, err)
	}
	return &DB{db: db, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.db.Close() }

// SQL is the underlying handle, for tests and for the state backup.
func (d *DB) SQL() *sql.DB { return d.db }

// SetClock replaces the clock (tests).
func (d *DB) SetClock(now func() time.Time) { d.now = now }

func (d *DB) ts() string { return d.now().UTC().Format(time.RFC3339Nano) }

// Migration is one numbered schema change.
type Migration struct {
	Version int
	Name    string
	SQL     string
	SHA256  string
}

var migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)

// Migrations returns the embedded migrations in order.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migration file %s: want NNNN_name.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(b)
		out = append(out, Migration{Version: v, Name: e.Name(), SQL: string(b), SHA256: hex.EncodeToString(h[:])})
	}
	slices.SortFunc(out, func(a, b Migration) int { return a.Version - b.Version })
	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migrations: %s, want version %d (no gaps, no duplicates)", m.Name, i+1)
		}
	}
	return out, nil
}

// Latest is the schema version this binary migrates to.
func Latest() int {
	ms, err := Migrations()
	if err != nil {
		return 0
	}
	return len(ms)
}

// AppliedMigration is a row of schema_migrations.
type AppliedMigration struct {
	Version   int    `json:"version"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	AppliedAt string `json:"applied_at"`
}

// Applied lists the migrations recorded in the database.
func (d *DB) Applied(ctx context.Context) ([]AppliedMigration, error) {
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, sha256 TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return nil, err
	}
	rows, err := d.db.QueryContext(ctx, `SELECT version, name, sha256, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppliedMigration
	for rows.Next() {
		var a AppliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.SHA256, &a.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Migrate applies the missing migrations, each in its own transaction, up to version to (0: Latest). It refuses a
// database whose applied migrations differ from this binary's (an edited migration) or that is newer than it.
func (d *DB) Migrate(ctx context.Context, to int) ([]int, error) {
	ms, err := Migrations()
	if err != nil {
		return nil, err
	}
	if to == 0 {
		to = len(ms)
	}
	if to < 0 || to > len(ms) {
		return nil, fmt.Errorf("migrate to %d: this binary knows versions 1-%d", to, len(ms))
	}
	applied, err := d.Applied(ctx)
	if err != nil {
		return nil, err
	}
	for i, a := range applied {
		switch {
		case a.Version != i+1:
			return nil, fmt.Errorf("schema_migrations: version %d where %d was expected", a.Version, i+1)
		case a.Version > len(ms):
			return nil, fmt.Errorf("the database is at schema version %d, newer than this binary (%d): use a newer gaia", applied[len(applied)-1].Version, len(ms))
		case a.SHA256 != ms[i].SHA256:
			return nil, fmt.Errorf("migration %d (%s) was applied with sha256 %s, this binary has %s", a.Version, a.Name, a.SHA256, ms[i].SHA256)
		}
	}
	if len(applied) > to {
		return nil, fmt.Errorf("the database is at schema version %d; migrations do not go backwards", len(applied))
	}
	var done []int
	for _, m := range ms[len(applied):to] {
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return done, err
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			tx.Rollback()
			return done, fmt.Errorf("migration %s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, sha256, applied_at) VALUES (?, ?, ?, ?)`,
			m.Version, m.Name, m.SHA256, d.ts()); err != nil {
			tx.Rollback()
			return done, err
		}
		if err := tx.Commit(); err != nil {
			return done, fmt.Errorf("migration %s: %w", m.Name, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}

// Schema returns the database's schema (tables, indexes, triggers) as normalised SQL, sorted: two databases with
// the same Schema have the same structure.
func (d *DB) Schema(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	space := regexp.MustCompile(`\s+`)
	for rows.Next() {
		var typ, name, s string
		if err := rows.Scan(&typ, &name, &s); err != nil {
			return nil, err
		}
		out = append(out, typ+" "+name+": "+space.ReplaceAllString(strings.TrimSpace(s), " "))
	}
	return out, rows.Err()
}

// classify maps SQLite constraint errors to ErrConflict / ErrInvalid, wrapping the original.
func classify(err error) error {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
		return fmt.Errorf("%w: a referenced or referencing record exists or is missing (%v)", ErrConflict, err)
	case sqlite3.SQLITE_CONSTRAINT_CHECK, sqlite3.SQLITE_CONSTRAINT_NOTNULL:
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return err
}

// tx runs f in a transaction and commits it, or rolls it back on any error.
func (d *DB) tx(ctx context.Context, f func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			return fmt.Errorf("%w (rollback: %v)", err, rerr)
		}
		return classify(err)
	}
	return classify(tx.Commit())
}
