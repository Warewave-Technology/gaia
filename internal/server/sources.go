package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Warewave-Technology/gaia/internal/source"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Sources runs the catalog's sources periodically and on demand, one
// replica at a time.
type Sources struct {
	Syncer  *source.Syncer
	Catalog *CatalogSource
	Store   *store.Store
	kick    chan struct{}
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
func (s *Sources) Run(ctx context.Context, every time.Duration) {
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
		return err
	})
	if err != nil && s.Syncer.Log != nil {
		s.Syncer.Log.Warn("sources", "err", err)
	}
	if wrote {
		s.Catalog.Kick()
	}
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
