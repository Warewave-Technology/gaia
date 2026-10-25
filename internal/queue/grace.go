package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// EndGraceArgs looks for stopped servers whose grace period is over.
type EndGraceArgs struct{}

func (EndGraceArgs) Kind() string { return "gaia.end_grace" }

// EndGraceWorker queues a decommission run for each contract with a
// server whose grace period is over (design 9.8).
type EndGraceWorker struct {
	river.WorkerDefaults[EndGraceArgs]
	Store *store.Store
	Now   func() time.Time
}

func (w *EndGraceWorker) Work(ctx context.Context, _ *river.Job[EndGraceArgs]) error {
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	contracts, err := w.Store.Q().DueContracts(ctx, pgtype.Timestamptz{Time: now, Valid: true})
	if err != nil {
		return err
	}
	c, err := river.ClientFromContextSafely[pgx.Tx](ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range contracts {
		if _, err := EnqueueDecommission(ctx, w.Store, c, name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// EndGraceEvery is the periodic job that ends grace periods, every
// fifteen minutes; the queue makes sure only one replica runs it.
func EndGraceEvery() *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return EndGraceArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true})
}
