package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// OperationArgs is an approved Operation waiting for its window.
type OperationArgs struct {
	Name        string        `json:"name"`
	Run         string        `json:"run"`
	ChangeID    string        `json:"change_id"`
	Snapshot    repo.Snapshot `json:"snapshot"`
	RequestedBy string        `json:"requested_by,omitempty"`
	ApprovedBy  []string      `json:"approved_by,omitempty"`
}

func (OperationArgs) Kind() string { return "gaia.operation" }

// InsertOpts: an Operation runs once; it is never retried by the queue.
func (OperationArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: Apply, MaxAttempts: 1}
}

// EnqueueOperation records the Operation as scheduled and queues its run
// for the start of its window (now when it has none).
func EnqueueOperation(ctx context.Context, s *store.Store, c *river.Client[pgx.Tx], op *plan.Operation, a OperationArgs, specHash string) error {
	a.Run = store.NewRunID()
	at := time.Now()
	if op.Window != nil && op.Window.Start.After(at) {
		at = op.Window.Start
	}
	return s.TxFunc(ctx, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.QueueRun(ctx, db.QueueRunParams{ID: a.Run, Contracts: op.Contracts, Commit: a.Snapshot.Label(), PlanHash: op.Hash, RequestedBy: text(a.RequestedBy)}); err != nil {
			return err
		}
		if err := q.InsertOperation(ctx, db.InsertOperationParams{Name: a.Name, Status: "scheduled", SpecHash: specHash, ChangeID: text(a.ChangeID), RunID: text(a.Run)}); err != nil {
			return err
		}
		res, err := c.InsertTx(ctx, tx, a, &river.InsertOpts{Queue: Apply, MaxAttempts: 1, ScheduledAt: at})
		if err != nil {
			return err
		}
		if err := q.SetRunJob(ctx, db.SetRunJobParams{ID: a.Run, JobID: pgtype.Int8{Int64: res.Job.ID, Valid: true}}); err != nil {
			return err
		}
		return q.SetChangeRun(ctx, db.SetChangeRunParams{ID: a.ChangeID, RunID: pgtype.Text{String: a.Run, Valid: true}})
	})
}

// OperationDone closes an Operation's change: status is succeeded,
// partial, failed, cancelled or expired.
type OperationDone func(ctx context.Context, a OperationArgs, status string) error

// OperationWorker runs Operations when their window opens.
type OperationWorker struct {
	river.WorkerDefaults[OperationArgs]
	Store    *store.Store
	Repos    *repo.Mirrors
	Systems  Systems
	Done     OperationDone
	BusyWait time.Duration
}

// Timeout: an Operation may take its whole window.
func (w *OperationWorker) Timeout(*river.Job[OperationArgs]) time.Duration { return 12 * time.Hour }

// Work runs the Operation, unless its time has passed.
func (w *OperationWorker) Work(ctx context.Context, job *river.Job[OperationArgs]) error {
	a := job.Args
	q := w.Store.Q()
	c, err := q.Change(ctx, a.ChangeID)
	if err != nil {
		return err
	}
	var op plan.Operation
	if err := json.Unmarshal(c.Plan, &op); err != nil {
		return river.JobCancel(err)
	}
	now := time.Now()
	late := op.NotAfter != nil && now.After(*op.NotAfter) || op.Window != nil && now.After(op.Window.End)
	if late {
		return w.end(ctx, a, "expired", fmt.Errorf("%s did not start in time", a.Name), nil)
	}
	for _, ct := range op.Contracts {
		busy, err := w.Store.ContractBusy(ctx, ct)
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
	}
	if _, err := q.SetOperationStatus(ctx, db.SetOperationStatusParams{Name: a.Name, Status: "running", FromStatuses: []string{"scheduled"}}); err != nil {
		return err
	}
	g, err := w.Repos.Graph(ctx, a.Snapshot)
	if err != nil {
		return w.end(ctx, a, "failed", err, nil)
	}
	deps, err := w.Systems(g)
	if err != nil {
		return w.end(ctx, a, "failed", err, nil)
	}
	deps.RunID, deps.RequestedBy, deps.ApprovedBy = a.Run, a.RequestedBy, a.ApprovedBy
	pr := a.Snapshot.Platform
	signed, _ := gitsrc.Signed(w.Repos.Path(pr.Repo()), pr.Commit)
	deps.Source = runrecord.Source{Repo: pr.URL, Commit: pr.Commit, Signed: signed}
	if cr := a.Snapshot.Config; cr != nil {
		if err := w.Repos.Ensure(ctx, cr.Repo(), cr.Commit); err != nil {
			return w.end(ctx, a, "failed", err, nil)
		}
		deps.Config = &apply.ConfigRepo{Path: w.Repos.Path(cr.Repo()), Commit: cr.Commit, Paths: cr.Paths}
	}
	_, out, err := apply.RunOperation(ctx, g, &op, deps)
	status := out.Status
	if status == "" {
		status = "failed"
		if ctx.Err() != nil {
			status = "cancelled"
		}
	}
	results, _ := json.Marshal(out.Results)
	if _, uerr := q.SetOperationStatus(context.WithoutCancel(ctx), db.SetOperationStatusParams{Name: a.Name, Status: status, Results: results, FromStatuses: []string{"running"}}); uerr != nil {
		err = errors.Join(err, uerr)
	}
	if w.Done != nil {
		if derr := w.Done(context.WithoutCancel(ctx), a, status); derr != nil {
			err = errors.Join(err, derr)
		}
	}
	if err != nil {
		return river.JobCancel(err)
	}
	return nil
}

// end closes an Operation that did not run.
func (w *OperationWorker) end(ctx context.Context, a OperationArgs, status string, cause error, results []byte) error {
	q := w.Store.Q()
	ctx = context.WithoutCancel(ctx)
	_, _ = q.TakeRun(ctx, a.Run)
	runStatus := "failed"
	if status == "expired" {
		runStatus = "cancelled"
	}
	err := errors.Join(q.FinishRun(ctx, db.FinishRunParams{ID: a.Run, Status: runStatus, Error: text(cause.Error())}),
		func() error {
			_, err := q.SetOperationStatus(ctx, db.SetOperationStatusParams{Name: a.Name, Status: status, Results: results, FromStatuses: []string{"scheduled", "running"}})
			return err
		}())
	if w.Done != nil {
		err = errors.Join(err, w.Done(ctx, a, status))
	}
	if err != nil {
		return river.JobCancel(errors.Join(cause, err))
	}
	return river.JobCancel(cause)
}
