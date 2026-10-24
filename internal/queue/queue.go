// Package queue runs applies as jobs (River, design 12.4): any worker
// that is free takes the next one; a contract that is busy waits.
package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Apply is the queue applies run in.
const Apply = "apply"

// DefaultBusyWait is how long a run waits when its contract is busy.
const DefaultBusyWait = 15 * time.Second

// ApplyArgs is one queued apply: one contract at one commit, with the plan
// hash it was queued with.
type ApplyArgs struct {
	Run         string   `json:"run"`
	Repo        string   `json:"repo"`
	Commit      string   `json:"commit"`
	Paths       []string `json:"paths,omitempty"`
	Contract    string   `json:"contract"`
	PlanHash    string   `json:"plan_hash"`
	RequestedBy string   `json:"requested_by,omitempty"`
}

func (ApplyArgs) Kind() string { return "gaia.apply" }

// InsertOpts: an apply is never retried by the queue. Its run is already
// recorded as failed, and the next one starts from a new plan.
func (ApplyArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: Apply, MaxAttempts: 1}
}

// Source is where a run reads its objects: a repository, a commit and the
// directories inside it.
type Source struct {
	Repo   string
	Commit string
	Paths  []string
}

// Graph loads and resolves the objects of src.
func (src Source) Graph() (*catalog.Graph, error) {
	files, commit, err := gitsrc.Load(src.Repo, src.Commit, src.Paths)
	if err != nil {
		return nil, err
	}
	cat, err := catalog.Load(commit, files)
	if err != nil {
		return nil, err
	}
	return catalog.Resolve(cat)
}

// Enqueue queues one run per contract of the plan, each with that
// contract's own plan. It returns the run IDs in the plan's order.
func Enqueue(ctx context.Context, s *store.Store, c *river.Client[pgx.Tx], g *catalog.Graph, src Source, contracts []string, requestedBy string) ([]string, error) {
	var args []ApplyArgs
	for _, name := range contracts {
		p, err := plan.Build(ctx, g, []string{name}, apply.StoreState{S: s})
		if err != nil {
			return nil, err
		}
		args = append(args, ApplyArgs{
			Run: store.NewRunID(), Repo: src.Repo, Commit: p.Commit, Paths: src.Paths,
			Contract: name, PlanHash: p.Hash, RequestedBy: requestedBy,
		})
	}
	err := s.TxFunc(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		for _, a := range args {
			if err := q.QueueRun(ctx, db.QueueRunParams{
				ID: a.Run, Contracts: []string{a.Contract}, Commit: a.Commit, PlanHash: a.PlanHash, RequestedBy: text(a.RequestedBy),
			}); err != nil {
				return err
			}
			res, err := c.InsertTx(ctx, tx, a, nil)
			if err != nil {
				return err
			}
			if err := q.SetRunJob(ctx, db.SetRunJobParams{ID: a.Run, JobID: pgtype.Int8{Int64: res.Job.ID, Valid: true}}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(args))
	for i, a := range args {
		ids[i] = a.Run
	}
	return ids, nil
}

// ErrFinished: the run has already ended.
var ErrFinished = errors.New("run has already finished")

// Cancel stops a run: a queued one never starts, a running one is
// interrupted where it is (its stage and record say so).
func Cancel(ctx context.Context, s *store.Store, c *river.Client[pgx.Tx], runID, by string) error {
	q := s.Q()
	run, err := q.Run(ctx, runID)
	if err != nil {
		return err
	}
	switch run.Status {
	case "queued", "running":
	default:
		return fmt.Errorf("%s: %w (%s)", runID, ErrFinished, run.Status)
	}
	if _, err := q.CancelQueuedRun(ctx, db.CancelQueuedRunParams{ID: runID, Error: text("cancelled by " + by + " before it started")}); err != nil {
		return err
	}
	if !run.JobID.Valid {
		return fmt.Errorf("%s: not a queued run", runID)
	}
	_, err = c.JobCancel(ctx, run.JobID.Int64)
	return err
}

// Systems builds the systems a run talks to for a graph: the secrets
// provider comes from its Platform.
type Systems func(g *catalog.Graph) (apply.Deps, error)

// ApplyWorker takes queued runs.
type ApplyWorker struct {
	river.WorkerDefaults[ApplyArgs]
	Store   *store.Store
	Systems Systems
	// MaxDuration bounds one run; the Platform's limits come later with
	// the change engine.
	MaxDuration time.Duration
	// BusyWait is how long a run waits when its contract is busy.
	BusyWait time.Duration
}

// Timeout is the longest a run may take.
func (w *ApplyWorker) Timeout(*river.Job[ApplyArgs]) time.Duration {
	if w.MaxDuration > 0 {
		return w.MaxDuration
	}
	return 2 * time.Hour
}

func (w *ApplyWorker) Work(ctx context.Context, job *river.Job[ApplyArgs]) error {
	a := job.Args
	busy, err := w.Store.ContractBusy(ctx, a.Contract)
	if err != nil {
		return err
	}
	if busy {
		wait := w.BusyWait
		if wait <= 0 {
			wait = DefaultBusyWait
		}
		return river.JobSnooze(wait)
	}
	if err := w.work(ctx, a); err != nil {
		// The run has its error; retrying would apply a stale plan.
		return river.JobCancel(err)
	}
	return nil
}

func (w *ApplyWorker) work(ctx context.Context, a ApplyArgs) error {
	src := Source{Repo: a.Repo, Commit: a.Commit, Paths: a.Paths}
	g, err := src.Graph()
	if err != nil {
		return w.fail(ctx, a, err)
	}
	// The plan is taken again: the state may have changed since the run was
	// queued, and then the queued plan is not the one to apply.
	p, err := plan.Build(ctx, g, []string{a.Contract}, apply.StoreState{S: w.Store})
	if err != nil {
		return w.fail(ctx, a, err)
	}
	if p.Hash != a.PlanHash {
		return w.fail(ctx, a, fmt.Errorf("the plan changed since the run was queued (%s, now %s)", short(a.PlanHash), short(p.Hash)))
	}
	deps, err := w.Systems(g)
	if err != nil {
		return w.fail(ctx, a, err)
	}
	signed, _ := gitsrc.Signed(a.Repo, a.Commit)
	deps.RunID, deps.RequestedBy = a.Run, a.RequestedBy
	deps.Source = runrecord.Source{Repo: a.Repo, Commit: a.Commit, Signed: signed}
	_, err = apply.Run(ctx, g, p, deps)
	return err
}

// fail ends a queued run that could not start.
func (w *ApplyWorker) fail(ctx context.Context, a ApplyArgs, cause error) error {
	q := w.Store.Q()
	if _, err := q.TakeRun(ctx, a.Run); err != nil {
		return errors.Join(cause, err)
	}
	if err := q.FinishRun(context.WithoutCancel(ctx), db.FinishRunParams{ID: a.Run, Status: "failed", Error: text(cause.Error())}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func short(h string) string {
	if len(h) > 19 {
		return h[:19]
	}
	return h
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
