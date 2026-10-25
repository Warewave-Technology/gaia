package source

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// OperationRetention is how long a finished Operation's file stays in
// Git (design 6.4); its record stays in the database and run records.
const OperationRetention = 30 * 24 * time.Hour

// CleanOperations has Gaia remove the files of Operations finished more
// than OperationRetention ago, one signed commit per repository.
func (s *Syncer) CleanOperations(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, now time.Time) error {
	if s.Writer == nil || s.Writer.Signer == nil {
		return nil
	}
	byRepo := map[string]map[string][]byte{}
	var names []string
	for _, o := range g.Catalog.List(catalog.KindOperation) {
		row, err := s.Store.Q().Operation(ctx, o.Metadata.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if !row.FinishedAt.Valid || now.Sub(row.FinishedAt.Time) < OperationRetention {
			continue
		}
		repoName, path := snap.Platform.Name, o.Source.File
		if r, p, ok := strings.Cut(o.Source.File, ":"); ok {
			repoName, path = r, p
		}
		if byRepo[repoName] == nil {
			byRepo[repoName] = map[string][]byte{}
		}
		byRepo[repoName][path] = nil
		byRepo[repoName][keepFile(path)] = []byte{}
		names = append(names, o.Metadata.Name)
	}
	if len(byRepo) == 0 {
		return nil
	}
	sort.Strings(names)
	revs := append([]repo.Rev{snap.Platform}, snap.Contracts...)
	var errs []error
	for repoName, files := range byRepo {
		i := slices.IndexFunc(revs, func(r repo.Rev) bool { return r.Name == repoName })
		if i < 0 {
			continue
		}
		msg := fmt.Sprintf("Remove finished Operations: %s\n\n%s: Operation cleanup\n", strings.Join(names, ", "), repo.TrailerSource)
		if _, err := s.Writer.Commit(ctx, revs[i].Repo(), revs[i].Commit, msg, files); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", repoName, err))
			continue
		}
		s.log().Info("operations removed", "repo", repoName, "operations", names)
	}
	return errors.Join(errs...)
}

// RemoveDecommissioned has Gaia remove the files of contracts whose
// decommission is over (design 9.6, step 2): decommissioned, applied, and
// without a server left. A file that holds other objects too is left for
// a person to edit.
func (s *Syncer) RemoveDecommissioned(ctx context.Context, snap repo.Snapshot, g *catalog.Graph) error {
	if s.Writer == nil || s.Writer.Signer == nil {
		return nil
	}
	byFile := map[string]int{}
	for _, k := range catalog.Kinds() {
		for _, o := range g.Catalog.List(k) {
			byFile[o.Source.File]++
		}
	}
	q := s.Store.Q()
	byRepo := map[string]map[string][]byte{}
	var names []string
	for _, o := range g.Catalog.List(catalog.KindServer) {
		spec := o.Spec.(*catalog.ServerSpec)
		if spec.Lifecycle == nil || spec.Lifecycle.State != "decommissioned" {
			continue
		}
		st, err := q.ContractState(ctx, o.Metadata.Name)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && st.Sync != "synced" {
			continue
		}
		if err != nil {
			return err
		}
		live, err := q.LiveHosts(ctx, o.Metadata.Name)
		if err != nil {
			return err
		}
		if len(live) > 0 {
			continue
		}
		if byFile[o.Source.File] > 1 {
			s.log().Info("decommissioned contract shares its file; not removed", "contract", o.Metadata.Name, "file", o.Source.File)
			continue
		}
		repoName, path := snap.Platform.Name, o.Source.File
		if r, p, ok := strings.Cut(o.Source.File, ":"); ok {
			repoName, path = r, p
		}
		if byRepo[repoName] == nil {
			byRepo[repoName] = map[string][]byte{}
		}
		byRepo[repoName][path] = nil
		// The directory stays even when this was its last file: a
		// followed path that is gone stops the catalog from loading.
		byRepo[repoName][keepFile(path)] = []byte{}
		names = append(names, o.Metadata.Name)
	}
	if len(byRepo) == 0 {
		return nil
	}
	sort.Strings(names)
	revs := append([]repo.Rev{snap.Platform}, snap.Contracts...)
	var errs []error
	for repoName, files := range byRepo {
		i := slices.IndexFunc(revs, func(r repo.Rev) bool { return r.Name == repoName })
		if i < 0 {
			continue
		}
		msg := fmt.Sprintf("Remove decommissioned contracts: %s\n\n%s: decommission\n", strings.Join(names, ", "), repo.TrailerSource)
		if _, err := s.Writer.Commit(ctx, revs[i].Repo(), revs[i].Commit, msg, files); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", repoName, err))
			continue
		}
		s.log().Info("decommissioned contracts removed", "repo", repoName, "contracts", names)
	}
	return errors.Join(errs...)
}

// keepFile is the empty file that keeps a directory in Git.
func keepFile(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i] + "/.gaia-keep"
	}
	return ".gaia-keep"
}
