package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/source"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Sources runs the catalog's sources periodically and on demand, one
// replica at a time.
type Sources struct {
	Syncer  *source.Syncer
	Catalog *CatalogSource
	Store   *store.Store
	// Queue takes the access runs that membership changes start; nil
	// starts none.
	Queue *river.Client[pgx.Tx]
	kick  chan struct{}
}

// Kick asks for a run now.
func (s *Sources) Kick() {
	select {
	case s.kicks() <- struct{}{}:
	default:
	}
}

func (s *Sources) kicks() chan struct{} {
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	return s.kick
}

// Run runs the sources every interval until ctx ends. A run that writes
// a commit refreshes the catalog, so its changes are evaluated at once.
// Team memberships are synced on their own, more often (Members).
func (s *Sources) Run(ctx context.Context, every time.Duration) {
	go s.members(ctx)
	kick := s.kicks()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
	}
}

func (s *Sources) once(ctx context.Context) {
	snap, err := s.Catalog.Snapshot()
	g := s.Catalog.Graph()
	if err != nil || g == nil {
		return
	}
	wrote := false
	err = s.Store.WithLock(ctx, "gaia/sources", func(ctx context.Context) error {
		var err error
		wrote, err = s.Syncer.Run(ctx, snap, g)
		if cerr := s.Syncer.CleanOperations(ctx, snap, g, time.Now()); cerr != nil {
			err = errors.Join(err, fmt.Errorf("operation cleanup: %w", cerr))
		}
		if cerr := s.Syncer.RemoveDecommissioned(ctx, snap, g); cerr != nil {
			err = errors.Join(err, fmt.Errorf("decommissioned contracts: %w", cerr))
		}
		return err
	})
	if err != nil && !errors.Is(err, store.ErrLocked) && s.Syncer.Log != nil {
		s.Syncer.Log.Warn("sources", "err", err)
	}
	if wrote {
		s.Catalog.Kick()
	}
}

// members syncs team memberships from the directories every
// membership.sync.every of the TeamSources (the shortest; 5m when none
// says).
func (s *Sources) members(ctx context.Context) {
	for {
		every := 5 * time.Minute
		if g := s.Catalog.Graph(); g != nil {
			for i, o := range g.Catalog.List(catalog.KindTeamSource) {
				if d := o.Spec.(*catalog.TeamSourceSpec).Every(); i == 0 || d < every {
					every = d
				}
			}
			err := s.Store.WithLock(ctx, "gaia/members", func(ctx context.Context) error {
				reps, err := s.Syncer.Memberships(ctx, g)
				if s.Queue == nil {
					return err
				}
				// The people changed: their contracts get their access again.
				runs, rerr := queue.Redistribute(ctx, s.Store, s.Queue, ChangedTeams(reps))
				if len(runs) > 0 && s.Syncer.Log != nil {
					s.Syncer.Log.Info("access runs", "runs", runs)
				}
				return errors.Join(err, rerr)
			})
			if err != nil && !errors.Is(err, store.ErrLocked) && s.Syncer.Log != nil {
				s.Syncer.Log.Warn("memberships", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// ChangedTeams are the teams whose people a membership sync changed.
func ChangedTeams(reps []source.Membership) []string {
	set := map[string]bool{}
	for _, r := range reps {
		for _, e := range append(append([]string{}, r.Joined...), r.Left...) {
			if team, _, ok := strings.Cut(e, "/"); ok {
				set[team] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// SourceReport is what a source did last.
type SourceReport struct {
	Source string          `json:"source"`
	Status string          `json:"status"`
	Commit string          `json:"commit,omitempty"`
	Detail json.RawMessage `json:"detail"`
	Error  string          `json:"error,omitempty"`
	RanAt  time.Time       `json:"ranAt"`
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Q().SourceReports(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]SourceReport, 0, len(rows))
	for _, x := range rows {
		out = append(out, SourceReport{Source: x.Source, Status: x.Status, Commit: x.Commit.String, Detail: json.RawMessage(x.Detail), Error: x.Error.String, RanAt: x.RanAt.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) syncSources(w http.ResponseWriter, _ *http.Request) {
	if s.cfg.Sources == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server runs no sources"})
		return
	}
	s.cfg.Sources.Kick()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "syncing"})
}
