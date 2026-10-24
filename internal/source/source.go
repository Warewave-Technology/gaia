// Package source turns outside systems into catalog objects (design 5):
// a Source reads its system, renders the objects it stands for, and when
// they differ from the platform repository Gaia writes them there in a
// signed commit. Gaia never writes a commit whose catalog does not load.
// What happened is kept as the source's report.
package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Secrets reads a bao:// reference at an OpenBao address.
type Secrets func(ctx context.Context, addr, ref string) (map[string]string, error)

// Syncer runs sources.
type Syncer struct {
	Store   *store.Store
	Repos   *repo.Mirrors
	Writer  *repo.Writer
	Secrets Secrets
	Log     *slog.Logger
}

func (s *Syncer) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Log
}

// Report is what one run of a source did.
type Report struct {
	Source   string   `json:"source"`
	Status   string   `json:"status"` // unchanged | committed | refused | failed
	Commit   string   `json:"commit,omitempty"`
	Added    []string `json:"added,omitempty"`
	Changed  []string `json:"changed,omitempty"`
	Removed  []string `json:"removed,omitempty"`
	Problems []string `json:"problems,omitempty"` // what was left out, and why
	Error    string   `json:"error,omitempty"`
}

// Run runs every source of the catalog once and records their reports.
// It returns whether any of them wrote a commit.
func (s *Syncer) Run(ctx context.Context, snap repo.Snapshot, g *catalog.Graph) (bool, error) {
	wrote := false
	var errs []error
	for _, o := range g.Catalog.List(catalog.KindTeamSource) {
		rep := s.teams(ctx, snap, g, o)
		wrote = wrote || rep.Status == "committed"
		if err := s.save(ctx, rep); err != nil {
			errs = append(errs, err)
		}
	}
	return wrote, errors.Join(errs...)
}

func (s *Syncer) save(ctx context.Context, r Report) error {
	detail, _ := json.Marshal(map[string]any{"added": r.Added, "changed": r.Changed, "removed": r.Removed, "problems": r.Problems})
	s.log().Info("source", "source", r.Source, "status", r.Status, "commit", r.Commit, "err", r.Error)
	return s.Store.Q().SetSourceReport(ctx, db.SetSourceReportParams{
		Source: r.Source, Status: r.Status, Commit: text(r.Commit), Detail: detail, Error: text(r.Error),
	})
}

// write compares a source's rendered files with its output directory at
// the snapshot and, when they differ and the result loads, commits them.
func (s *Syncer) write(ctx context.Context, snap repo.Snapshot, rep *Report, output string, want map[string][]byte) {
	plat := snap.Platform
	current, _, err := gitsrc.Load(s.Repos.Path(plat.Repo()), plat.Commit, []string{output})
	if err != nil && !errors.Is(err, gitsrc.ErrNoDirectory) {
		rep.Status, rep.Error = "failed", err.Error()
		return
	}
	have := map[string][]byte{}
	for _, f := range current {
		have[f.Path] = f.Data
	}
	changes := map[string][]byte{}
	for p, data := range want {
		old, ok := have[p]
		switch {
		case !ok:
			rep.Added = append(rep.Added, p)
			changes[p] = data
		case !bytes.Equal(old, data):
			rep.Changed = append(rep.Changed, p)
			changes[p] = data
		}
	}
	for p := range have {
		if _, ok := want[p]; !ok {
			rep.Removed = append(rep.Removed, p)
			changes[p] = nil
		}
	}
	sort.Strings(rep.Added)
	sort.Strings(rep.Changed)
	sort.Strings(rep.Removed)
	if len(changes) == 0 {
		rep.Status = "unchanged"
		return
	}
	if _, err := s.Repos.GraphWith(ctx, snap, changes); err != nil {
		rep.Status, rep.Error = "refused", "the catalog would not load: "+err.Error()
		return
	}
	if s.Writer == nil || s.Writer.Signer == nil {
		rep.Status, rep.Error = "refused", "Gaia has no signing key to write the change (gaia server --signing-key)"
		return
	}
	msg := fmt.Sprintf("%s: %d added, %d changed, %d removed\n\n%s: %s\n", rep.Source, len(rep.Added), len(rep.Changed), len(rep.Removed), repo.TrailerSource, rep.Source)
	h, err := s.Writer.Commit(ctx, plat.Repo(), plat.Commit, msg, changes)
	if err != nil {
		rep.Status, rep.Error = "failed", err.Error()
		return
	}
	rep.Status, rep.Commit = "committed", h
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func file(output, name string) string { return path.Join(strings.Trim(output, "/"), name+".yaml") }
