package queue

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/provider/check"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// CheckProvidersArgs checks every provider of the catalog.
type CheckProvidersArgs struct{}

func (CheckProvidersArgs) Kind() string { return "gaia.check_providers" }

// InsertOpts keeps to one check a minute, however often it is asked for.
func (CheckProvidersArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
}

// CheckProvidersWorker records whether Gaia reaches each provider with
// its credentials (design 11.5).
type CheckProvidersWorker struct {
	river.WorkerDefaults[CheckProvidersArgs]
	Store   *store.Store
	Graph   func() *catalog.Graph
	Systems Systems
}

func (w *CheckProvidersWorker) Timeout(*river.Job[CheckProvidersArgs]) time.Duration {
	return 2 * check.Timeout
}

func (w *CheckProvidersWorker) Work(ctx context.Context, _ *river.Job[CheckProvidersArgs]) error {
	g := w.Graph()
	if g == nil {
		return nil
	}
	providers := g.Catalog.List(catalog.KindProvider)
	results := make([]check.Result, len(providers))
	d, err := w.Systems(g)
	if err != nil {
		// Without the secret store no provider can be checked.
		for i := range results {
			results[i] = check.Result{Status: check.Failed, Error: "secrets: " + err.Error()}
		}
	} else {
		var wg sync.WaitGroup
		for i, p := range providers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = check.Run(ctx, p.Spec.(*catalog.ProviderSpec), d.Secrets)
			}()
		}
		wg.Wait()
	}
	q := w.Store.Q()
	keep := make([]string, 0, len(providers))
	for i, p := range providers {
		r := results[i]
		keep = append(keep, p.Metadata.Name)
		if err := q.SetProviderCheck(ctx, db.SetProviderCheckParams{
			Provider: p.Metadata.Name, Type: p.Spec.(*catalog.ProviderSpec).Type, Status: r.Status,
			Detail: pgtype.Text{String: r.Detail, Valid: r.Detail != ""}, Error: pgtype.Text{String: r.Error, Valid: r.Error != ""},
			LatencyMs: int32(r.Latency / time.Millisecond), //nolint:gosec // bounded by the timeout
		}); err != nil {
			return err
		}
	}
	return q.DeleteProviderChecksExcept(ctx, keep)
}

// CheckProvidersEvery checks the providers every five minutes, and once
// when the server starts.
func CheckProvidersEvery() *river.PeriodicJob {
	return river.NewPeriodicJob(river.PeriodicInterval(5*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return CheckProvidersArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true})
}
