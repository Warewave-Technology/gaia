package apply

import (
	"context"
	"fmt"
	"time"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
)

// writeRecord appends the run's record to the chain and, when the
// Platform names a run archive, stores a copy there.
func (r *runner) writeRecord(ctx context.Context, p *plan.Plan, status, msg string) error {
	var contracts []string
	for _, c := range p.Contracts {
		contracts = append(contracts, c.Name)
	}
	rec := runrecord.Record{
		Run: r.run, RequestedBy: r.d.RequestedBy, ApprovedBy: append([]string{}, r.d.ApprovedBy...),
		Source: r.d.Source, CatalogCommit: p.Commit,
		Contracts: contracts, PlanHash: p.Hash, ExecutedAs: r.executedAs,
		StartedAt: r.started, FinishedAt: time.Now().UTC(), Result: status, Error: msg,
		Changes: r.changes,
	}
	if rec.Source.Commit == "" {
		rec.Source.Commit = p.Commit
	}
	if r.changes == nil {
		rec.Changes = []runrecord.Change{}
	}
	if r.cfg != nil {
		rec.ConfigCommit, rec.ExecutionEnvironment = r.cfg.commit, r.cfg.image
	}
	written, err := runrecord.Append(ctx, r.d.Store, rec)
	if err != nil {
		return fmt.Errorf("run record: %w", err)
	}
	r.printf("record %s\n", written.RecordHash)

	name := r.g.Platform().Spec.(*catalog.PlatformSpec).RunArchive
	if name == "" {
		return nil
	}
	a := r.provider(name)
	retention, err := catalog.ParseRetention(a.Retention)
	if err != nil {
		return fmt.Errorf("%s: retention: %w", name, err)
	}
	creds, err := r.d.Secrets.Read(ctx, a.Credentials)
	if err != nil {
		return fmt.Errorf("run archive: %w", err)
	}
	archive, err := r.d.Archive(a.Endpoint, a.Bucket, creds["access_key"], creds["secret_key"])
	if err != nil {
		return fmt.Errorf("run archive: %w", err)
	}
	loc, err := runrecord.Store(ctx, r.d.Store, archive, written, retention)
	if err != nil {
		return fmt.Errorf("run archive: %w (the record is in the database)", err)
	}
	r.printf("archived %s (COMPLIANCE, %s)\n", loc, a.Retention)
	return nil
}
