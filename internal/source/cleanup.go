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
