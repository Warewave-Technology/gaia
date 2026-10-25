// Package settings is Gaia's settings store (design 17.2): the objects
// that are Gaia's settings rather than Git's, as the YAML a repository
// would hold, in numbered versions that are never changed. Every version
// holds every object in full; writing one is audited.
package settings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/audit"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// ErrStale is a write based on a version that is no longer the latest.
var ErrStale = errors.New("the settings changed meanwhile")

// Store reads and writes the settings versions.
type Store struct {
	DB *store.Store

	mu    sync.Mutex
	cache map[int64][]catalog.File
}

// Latest is the latest version, 0 before the first.
func (s *Store) Latest(ctx context.Context) (int64, error) {
	return s.DB.Q().LatestSettingsVersion(ctx)
}

// Files is a version's files, by path. Versions never change, so they
// are kept once read.
func (s *Store) Files(ctx context.Context, version int64) ([]catalog.File, error) {
	s.mu.Lock()
	if fs, ok := s.cache[version]; ok {
		s.mu.Unlock()
		return fs, nil
	}
	s.mu.Unlock()
	rows, err := s.DB.Q().SettingsObjects(ctx, version)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		if _, err := s.DB.Q().SettingsVersion(ctx, version); err != nil {
			return nil, fmt.Errorf("settings version %d: %w", version, err)
		}
	}
	fs := make([]catalog.File, 0, len(rows))
	for _, r := range rows {
		fs = append(fs, catalog.File{Path: r.Path, Data: []byte(r.Data)})
	}
	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[int64][]catalog.File{}
	}
	s.cache[version] = fs
	s.mu.Unlock()
	return fs, nil
}

// Write is a new version: Base's files with Changes applied (nil
// removes a file).
type Write struct {
	Base     int64
	Changes  map[string][]byte
	Author   string // who: a username, gaia, or source/<name>
	Reason   string
	Proposal string
	// Audit is the entry the write is logged as; its Detail is filled in
	// with the version and the paths.
	Audit audit.Entry
}

// Diff is what a write did, by path.
type Diff struct {
	Version int64    `json:"version"`
	Added   []string `json:"added,omitempty"`
	Changed []string `json:"changed,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// Apply returns Base's files with the changes applied, and what changed.
func Apply(base []catalog.File, changes map[string][]byte) ([]catalog.File, Diff) {
	have := map[string][]byte{}
	for _, f := range base {
		have[f.Path] = f.Data
	}
	var d Diff
	for p, data := range changes {
		old, ok := have[p]
		switch {
		case data == nil && ok:
			d.Removed = append(d.Removed, p)
			delete(have, p)
		case data == nil:
		case !ok:
			d.Added = append(d.Added, p)
			have[p] = data
		case string(old) != string(data):
			d.Changed = append(d.Changed, p)
			have[p] = data
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Changed)
	sort.Strings(d.Removed)
	out := make([]catalog.File, 0, len(have))
	for p, data := range have {
		out = append(out, catalog.File{Path: p, Data: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, d
}

// Empty reports whether a diff changes nothing.
func (d Diff) Empty() bool { return len(d.Added)+len(d.Changed)+len(d.Removed) == 0 }

// Hash is a version's content hash.
func Hash(files []catalog.File) string {
	h := sha256.New()
	for _, f := range files {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", f.Path, len(f.Data))
		_, _ = h.Write(f.Data)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Write stores a new version and its audit entry together. It refuses
// with ErrStale when Base is not the latest version, and writes nothing
// when the changes change nothing (the version is then 0).
func (s *Store) Write(ctx context.Context, w Write) (Diff, error) {
	var d Diff
	for p := range w.Changes {
		if !catalog.FromSettings(p) || !catalog.IsSetting(catalog.SettingsFileKind(p)) {
			return d, fmt.Errorf("%s: not a settings file", p)
		}
	}
	err := s.DB.WaitLock(ctx, "settings", func(ctx context.Context) error {
		return s.DB.Tx(ctx, func(q *db.Queries) error {
			latest, err := q.LatestSettingsVersion(ctx)
			if err != nil {
				return err
			}
			if latest != w.Base {
				return fmt.Errorf("%w: version %d is the latest, not %d", ErrStale, latest, w.Base)
			}
			var base []catalog.File
			if w.Base > 0 {
				rows, err := q.SettingsObjects(ctx, w.Base)
				if err != nil {
					return err
				}
				for _, r := range rows {
					base = append(base, catalog.File{Path: r.Path, Data: []byte(r.Data)})
				}
			}
			files, diff := Apply(base, w.Changes)
			if diff.Empty() {
				return nil
			}
			diff.Version = latest + 1
			if err := q.InsertSettingsVersion(ctx, db.InsertSettingsVersionParams{
				Version: diff.Version, Author: w.Author, Reason: w.Reason,
				Proposal: pgtype.Text{String: w.Proposal, Valid: w.Proposal != ""}, ContentHash: Hash(files),
			}); err != nil {
				return err
			}
			for _, f := range files {
				if err := q.InsertSettingsObject(ctx, db.InsertSettingsObjectParams{Version: diff.Version, Path: f.Path, Data: string(f.Data)}); err != nil {
					return err
				}
			}
			e := w.Audit
			if e.Actor == "" {
				e.Actor = w.Author
			}
			if e.Action == "" {
				e.Action = "settings.write"
			}
			if e.Object == "" {
				e.Object = fmt.Sprintf("settings@%d", diff.Version)
			}
			e.Detail = map[string]any{"version": diff.Version, "base": w.Base, "reason": w.Reason, "added": diff.Added, "changed": diff.Changed, "removed": diff.Removed, "detail": e.Detail}
			if err := audit.Write(ctx, q, e); err != nil {
				return err
			}
			d = diff
			return nil
		})
	})
	return d, err
}
