// Package drift finds where the systems no longer are what a run applied
// (design 7.5), records it, tells the owners, and opens the change that
// puts it right: at once or after the owner's approval, as the Policy
// says per area and environment.
package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Check finds the drift of one area in an applied plan.
type Check func(ctx context.Context, g *catalog.Graph, p *plan.Plan, d apply.Deps) ([]apply.Finding, error)

// Checks are the areas Gaia checks, by name.
var Checks = map[string]Check{
	"registration":  apply.CheckRegistration,
	"provisioning":  apply.CheckProvisioning,
	"configuration": apply.CheckConfiguration,
}

// Scanner checks the areas when they are due.
type Scanner struct {
	Store   *store.Store
	Repos   *repo.Mirrors
	Systems queue.Systems
	// Policy is the Policy in force: how often areas are checked.
	Policy func() *catalog.PolicySpec
	// Open opens the change that puts a contract's drift right
	// (change.Engine.OpenDrift).
	Open   func(ctx context.Context, contract string, auto bool) (string, error)
	Notify *notify.Notifier
	Log    *slog.Logger
	Now    func() time.Time
	// Checks overrides the areas, for tests.
	Checks map[string]Check
	// Graph is the desired state now, for discovery; without it there is
	// no discovery.
	Graph func() *catalog.Graph
}

// DiscoveryArea names discovery among the scans; it runs once a day, or
// at once after a request (POST /api/v1/discovery).
const DiscoveryArea = "discovery"

// discover writes a discovery report when one is due.
func (s *Scanner) discover(ctx context.Context) error {
	if s.Graph == nil || s.Systems == nil {
		return nil
	}
	g := s.Graph()
	if g == nil {
		return nil
	}
	return s.Store.WithLock(ctx, "gaia/drift/"+DiscoveryArea, func(ctx context.Context) error {
		q := s.Store.Q()
		last, err := q.DriftScanned(ctx, DiscoveryArea)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		now := s.now()
		if err == nil && now.Sub(last.Time) < 24*time.Hour {
			return nil
		}
		deps, err := s.Systems(g)
		if err != nil {
			return err
		}
		rep, err := apply.Discover(ctx, g, deps)
		if err != nil {
			return err
		}
		data, err := json.Marshal(rep)
		if err != nil {
			return err
		}
		if err := q.InsertDiscovery(ctx, data); err != nil {
			return err
		}
		if err := q.PruneDiscovery(ctx); err != nil {
			return err
		}
		s.log().Info("discovery", "counts", rep.Counts, "errors", len(rep.Errors))
		return q.SetDriftScanned(ctx, db.SetDriftScannedParams{Area: DiscoveryArea, LastAt: pgtype.Timestamptz{Time: now, Valid: true}})
	})
}

func (s *Scanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scanner) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Log
}

// Run checks every tick which areas are due, until ctx ends.
func (s *Scanner) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log().Warn("drift", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick scans the areas whose time has come; one replica does each, the
// others skip it.
func (s *Scanner) Tick(ctx context.Context) error {
	checks := s.Checks
	if checks == nil {
		checks = Checks
	}
	var pol *catalog.PolicySpec
	if s.Policy != nil {
		pol = s.Policy()
	}
	areas := make([]string, 0, len(checks))
	for a := range checks {
		areas = append(areas, a)
	}
	sort.Strings(areas)
	var errs []error
	for _, area := range areas {
		err := s.Store.WithLock(ctx, "gaia/drift/"+area, func(ctx context.Context) error {
			q := s.Store.Q()
			last, err := q.DriftScanned(ctx, area)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			now := s.now()
			if err == nil && now.Sub(last.Time) < pol.DriftEvery(area) {
				return nil
			}
			if w := pol.DriftWindow(area); w != "" && !catalog.InDailyWindow(w, now) {
				return nil
			}
			if err := s.Scan(ctx, area, checks[area]); err != nil {
				return err
			}
			return q.SetDriftScanned(ctx, db.SetDriftScannedParams{Area: area, LastAt: pgtype.Timestamptz{Time: now, Valid: true}})
		})
		if err != nil && !errors.Is(err, store.ErrLocked) {
			errs = append(errs, fmt.Errorf("%s: %w", area, err))
		}
	}
	if err := s.discover(ctx); err != nil && !errors.Is(err, store.ErrLocked) {
		errs = append(errs, fmt.Errorf("discovery: %w", err))
	}
	return errors.Join(errs...)
}

// Scan checks one area of every applied contract.
func (s *Scanner) Scan(ctx context.Context, area string, check Check) error {
	q := s.Store.Q()
	states, err := q.ContractStates(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, st := range states {
		if len(st.AppliedPlan) == 0 {
			continue
		}
		if err := s.contract(ctx, area, check, st); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", st.Contract, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Scanner) contract(ctx context.Context, area string, check Check, st db.ContractState) error {
	var p plan.Plan
	if err := json.Unmarshal(st.AppliedPlan, &p); err != nil {
		return err
	}
	var snap repo.Snapshot
	if err := json.Unmarshal(st.AppliedSnapshot, &snap); err != nil {
		return err
	}
	g, err := s.Repos.Graph(ctx, snap)
	if err != nil {
		return err
	}
	deps, err := s.Systems(g)
	if err != nil {
		return err
	}
	if cr := snap.Config; cr != nil {
		// Roles as the run applied them.
		if err := s.Repos.Ensure(ctx, cr.Repo(), cr.Commit); err != nil {
			return err
		}
		deps.Config = &apply.ConfigRepo{Path: s.Repos.Path(cr.Repo()), Commit: cr.Commit, Paths: cr.Paths}
	}
	// A contract a run is changing is not checked (design 7.5).
	var found []apply.Finding
	err = s.Store.WithContractLock(ctx, st.Contract, func(ctx context.Context) error {
		var err error
		found, err = check(ctx, g, &p, deps)
		return err
	})
	if errors.Is(err, store.ErrLocked) {
		return nil
	}
	if err != nil {
		return err
	}
	q := s.Store.Q()
	seen := []int64{} // not nil: none seen resolves every report
	var freshIDs []int64
	var fresh []apply.Finding
	open := 0
	for _, f := range found {
		status := "open"
		if f.Unknown {
			status = "unknown"
		} else {
			open++
		}
		row, err := q.SeeDrift(ctx, db.SeeDriftParams{Contract: f.Contract, Uid: f.UID, Hostname: f.Hostname, Area: area, Key: f.Key, Want: f.Want, Got: f.Got, Status: status})
		if err != nil {
			return err
		}
		seen = append(seen, row.ID)
		if row.New && !f.Unknown {
			fresh = append(fresh, f)
			freshIDs = append(freshIDs, row.ID)
		}
	}
	resolved, err := q.ResolveDrift(ctx, db.ResolveDriftParams{Contract: st.Contract, Area: area, Seen: seen})
	if err != nil {
		return err
	}
	if len(found)+len(resolved) > 0 {
		s.log().Info("drift", "contract", st.Contract, "area", area, "open", open, "new", len(fresh), "resolved", len(resolved))
	}
	// How far Gaia goes (design 8.1): observe records, report also tells,
	// enforce puts it right.
	level := "enforce"
	for _, c := range p.Contracts {
		if c.Name == st.Contract {
			level = c.Level(area)
		}
	}
	if open == 0 {
		// Gone by itself: its change has nothing left to put right.
		done := map[string]bool{}
		for _, r := range resolved {
			if r.ChangeID.Valid && !done[r.ChangeID.String] {
				done[r.ChangeID.String] = true
				if _, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: r.ChangeID.String, Status: "superseded", FromStatuses: []string{"pending", "approved"}}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	env := ""
	if c, ok := g.Catalog.Get(catalog.KindServer, st.Contract); ok {
		env = c.Metadata.Labels["env"]
	}
	var pol *catalog.PolicySpec
	if o := g.Policy(); o != nil {
		pol = o.Spec.(*catalog.PolicySpec)
	}
	change := ""
	if s.Open != nil && level == "enforce" {
		auto := pol.DriftRemediation(area, env) == "auto"
		if change, err = s.Open(ctx, st.Contract, auto); err != nil {
			return err
		}
		if change != "" {
			if err := q.SetDriftChange(ctx, db.SetDriftChangeParams{ChangeID: pgtype.Text{String: change, Valid: true}, Contract: st.Contract, Area: area}); err != nil {
				return err
			}
		}
	}
	if len(fresh) > 0 && s.Notify != nil && level != "observe" {
		var lines []string
		for _, f := range fresh {
			lines = append(lines, fmt.Sprintf("%s %s: %s yerine %s", f.Hostname, f.Key, f.Want, f.Got))
		}
		ev := notify.Event{Trigger: "drift.found", Contract: st.Contract, Status: area, Change: change, Comment: strings.Join(lines, "\n"),
			Key: fmt.Sprintf("drift/%s/%s/%d", st.Contract, area, freshIDs[0])}
		if err := s.Notify.Emit(ctx, g, ev); err != nil {
			s.log().Warn("drift notification", "contract", st.Contract, "err", err)
		}
	}
	return nil
}
