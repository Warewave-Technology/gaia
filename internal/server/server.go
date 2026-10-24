// Package server is gaia server: the API and the queue's workers in one
// process, or either of them alone (design 13.1). Replicas share nothing
// but the database.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/change"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Config is how one server process runs.
type Config struct {
	Listen  string // API address; empty runs no API
	Workers int    // concurrent runs; 0 runs no worker
	// RunTimeout bounds one run.
	RunTimeout time.Duration
	// StopTimeout is how long running jobs get to finish on shutdown
	// before they are cancelled.
	StopTimeout time.Duration
	// BusyWait is how long a run waits when its contract is busy.
	BusyWait time.Duration
	// EventRetention is how long run events stay in the database.
	EventRetention time.Duration
	Systems        queue.Systems
	Log            *slog.Logger
	// Auth signs people in and guards the API. Without it the API is open,
	// which the CLI allows only on loopback addresses.
	Auth *auth.Service
	// Repos are the local mirrors workers read snapshots from.
	Repos *repo.Mirrors
	// Finished is told about every run that ends (the change engine).
	Finished func(context.Context, queue.ApplyArgs, *catalog.Graph) error
	// Changes is the change engine and Catalog the desired state it
	// follows; the changes API needs both.
	Changes *change.Engine
	Catalog *CatalogSource
	// Writer writes Gaia's own commits (reverts); nil when Gaia has no
	// signing key.
	Writer *repo.Writer
	// NotifySecrets reads channel credentials for the notification
	// workers.
	NotifySecrets notify.Secrets
	// WebhookSecret checks GitHub's push webhooks; empty turns them off.
	WebhookSecret []byte
}

// Server is a running gaia server.
type Server struct {
	cfg   Config
	store *store.Store
	queue *river.Client[pgx.Tx]
	mux   *http.ServeMux
}

// New prepares a server. Nothing runs until Run.
func New(cfg Config, s *store.Store) (*Server, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Listen == "" && cfg.Workers == 0 {
		return nil, errors.New("server: neither API nor workers")
	}
	rc := &river.Config{Logger: cfg.Log}
	if cfg.Workers > 0 {
		workers := river.NewWorkers()
		if err := river.AddWorkerSafely(workers, &queue.ApplyWorker{Store: s, Repos: cfg.Repos, Finished: cfg.Finished, Systems: cfg.Systems, MaxDuration: cfg.RunTimeout, BusyWait: cfg.BusyWait}); err != nil {
			return nil, err
		}
		if err := river.AddWorkerSafely(workers, &queue.PruneEventsWorker{Store: s, Retention: cfg.EventRetention}); err != nil {
			return nil, err
		}
		if err := river.AddWorkerSafely(workers, &notify.SendWorker{Store: s, Secrets: cfg.NotifySecrets}); err != nil {
			return nil, err
		}
		rc.Workers = workers
		rc.Queues = map[string]river.QueueConfig{
			queue.Apply:        {MaxWorkers: cfg.Workers},
			river.QueueDefault: {MaxWorkers: 4},
		}
		rc.PeriodicJobs = []*river.PeriodicJob{queue.PruneEventsDaily()}
	}
	c, err := river.NewClient(riverpgxv5.New(s.Pool()), rc)
	if err != nil {
		return nil, err
	}
	srv := &Server{cfg: cfg, store: s, queue: c, mux: http.NewServeMux()}
	srv.routes()
	return srv, nil
}

// Handler is the HTTP API.
func (s *Server) Handler() http.Handler { return s.mux }

// Queue is the job queue's client, for enqueuing and cancelling.
func (s *Server) Queue() *river.Client[pgx.Tx] { return s.queue }

// Run serves until ctx ends, then stops: the API at once, the workers
// after their runs finish or StopTimeout passes.
func (s *Server) Run(ctx context.Context) error {
	errc := make(chan error, 2)
	if s.cfg.Workers > 0 {
		if err := s.queue.Start(ctx); err != nil {
			return err
		}
		s.cfg.Log.Info("workers started", "concurrent", s.cfg.Workers)
	}
	var hs *http.Server
	if s.cfg.Listen != "" {
		ln, err := net.Listen("tcp", s.cfg.Listen)
		if err != nil {
			return err
		}
		hs = &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
		s.cfg.Log.Info("api listening", "addr", ln.Addr().String())
		go func() {
			if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.stopTimeout())
	defer cancel()
	if hs != nil {
		err = errors.Join(err, hs.Shutdown(stop))
	}
	if s.cfg.Workers > 0 {
		if serr := s.queue.Stop(stop); serr != nil {
			// Runs still going are cancelled; their records say so.
			s.cfg.Log.Warn("runs did not finish in time; cancelling", "err", serr)
			cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer ccancel()
			err = errors.Join(err, s.queue.StopAndCancel(cctx))
		}
	}
	return err
}

func (s *Server) stopTimeout() time.Duration {
	if s.cfg.StopTimeout > 0 {
		return s.cfg.StopTimeout
	}
	return 5 * time.Minute
}

func (s *Server) routes() {
	for _, rt := range s.apiRoutes() {
		h := rt.h
		if s.cfg.Auth != nil && rt.role != public {
			h = s.cfg.Auth.Guard(rt.role, h)
		}
		s.mux.HandleFunc(rt.method+" "+rt.path, h)
	}
	if s.cfg.Auth != nil {
		s.cfg.Auth.Mount(s.mux)
	}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Pool().Ping(r.Context()); err != nil {
			http.Error(w, "database: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintln(w, "ready")
	})
}
