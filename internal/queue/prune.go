package queue

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// DefaultEventRetention is how long run events stay in the database.
const DefaultEventRetention = 90 * 24 * time.Hour

// PruneEventsArgs deletes run events older than the retention.
type PruneEventsArgs struct{}

func (PruneEventsArgs) Kind() string { return "gaia.prune_events" }

// PruneEventsWorker deletes old run events.
type PruneEventsWorker struct {
	river.WorkerDefaults[PruneEventsArgs]
	Store     *store.Store
	Retention time.Duration
}

func (w *PruneEventsWorker) Work(ctx context.Context, _ *river.Job[PruneEventsArgs]) error {
	keep := w.Retention
	if keep <= 0 {
		keep = DefaultEventRetention
	}
	_, err := w.Store.Q().PruneRunEvents(ctx, pgtype.Timestamptz{Time: time.Now().Add(-keep), Valid: true})
	return err
}

// PruneEventsDaily is the periodic job that prunes events once a day; the
// queue makes sure only one replica runs it.
func PruneEventsDaily() *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return PruneEventsArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true})
}
