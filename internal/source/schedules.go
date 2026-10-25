package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/teambition/rrule-go"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// schedules writes an OperationSchedule's Operations (design 6.4): one
// for each window starting within createAhead, named after the schedule
// and the window's date. Operations written before stay as they are;
// finished ones are removed by the cleanup.
func (s *Syncer) schedules(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, o *catalog.Object, now time.Time) Report {
	rep := Report{Source: catalog.KindSchedule + "/" + o.Metadata.Name}
	fail := func(err error) Report {
		rep.Status, rep.Error = "failed", err.Error()
		return rep
	}
	spec := o.Spec.(*catalog.OperationScheduleSpec)
	windows, err := Windows(spec, now)
	if err != nil {
		return fail(err)
	}
	dur, _ := time.ParseDuration(spec.Duration)
	plat := snap.Platform
	current, _, err := gitsrc.Load(s.Repos.Path(plat.Repo()), plat.Commit, []string{spec.Output})
	if err != nil && !errors.Is(err, gitsrc.ErrNoDirectory) {
		return fail(err)
	}
	want := map[string][]byte{}
	for _, f := range current {
		want[f.Path] = f.Data // what was written stays
	}
	for _, start := range windows {
		name := o.Metadata.Name + "-" + strings.ToLower(start.Format("20060102t1504"))
		if _, ok := g.Catalog.Get(catalog.KindOperation, name); ok {
			continue
		}
		if _, err := s.Store.Q().Operation(ctx, name); err == nil {
			continue // it ran, and its file was cleaned up
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fail(err)
		}
		end := start.Add(dur)
		t := spec.Template
		op := &catalog.OperationSpec{
			Action: t.Action, Selector: t.Selector, Strategy: t.Strategy, Options: t.Options, Runbook: t.Runbook, Params: t.Params,
			Window:   &catalog.Window{Start: start.Format(time.RFC3339), End: end.Format(time.RFC3339)},
			NotAfter: end.Format(time.RFC3339),
		}
		data, err := catalog.Render(catalog.KindOperation, name, nil, op, rep.Source, start.UTC().Format(time.RFC3339))
		if err != nil {
			return fail(err)
		}
		want[file(spec.Output, name)] = data
	}
	s.write(ctx, snap, &rep, spec.Output, want)
	return rep
}

// Windows are the starts of a schedule's windows from now to createAhead
// on, in its time zone.
func Windows(spec *catalog.OperationScheduleSpec, now time.Time) ([]time.Time, error) {
	loc, err := spec.Location()
	if err != nil {
		return nil, err
	}
	at, err := time.Parse("15:04", spec.Start)
	if err != nil {
		return nil, err
	}
	opt, err := rrule.StrToROption(spec.Recurrence)
	if err != nil {
		return nil, fmt.Errorf("recurrence: %w", err)
	}
	// The rule counts from a year back, at the start time of day.
	n := now.In(loc).AddDate(-1, 0, 0)
	opt.Dtstart = time.Date(n.Year(), n.Month(), n.Day(), at.Hour(), at.Minute(), 0, 0, loc)
	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, fmt.Errorf("recurrence: %w", err)
	}
	return rule.Between(now, now.Add(spec.Ahead()), true), nil
}
