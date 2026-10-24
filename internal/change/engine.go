// Package change is Gaia's backbone (design 7.6): it compares each
// contract's plan at the head of Git with what was last applied, keeps one
// Change per contract that differs, lists what the Change needs, and
// queues the run once everything is there.
package change

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Change statuses.
const (
	Pending    = "pending"
	Approved   = "approved"
	Running    = "running"
	Applied    = "applied"
	Failed     = "failed"
	Cancelled  = "cancelled"
	Rejected   = "rejected"
	Superseded = "superseded"
)

// Gate statuses.
const (
	OK      = "ok"
	Waiting = "waiting"
	Blocked = "failed"
)

// lockName is held while changes are evaluated or decided.
const lockName = "gaia/change-engine"

// Engine evaluates changes.
type Engine struct {
	Store *store.Store
	Repos *repo.Mirrors
	Queue *river.Client[pgx.Tx]
	// Keys are the registered signing keys.
	Keys func(context.Context) ([]signature.Key, error)
	Log  *slog.Logger
}

func (e *Engine) log() *slog.Logger {
	if e.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return e.Log
}

// Evaluate brings every contract's change in line with a snapshot. It is
// safe to call often: one replica evaluates at a time and the others skip.
func (e *Engine) Evaluate(ctx context.Context, snap repo.Snapshot, g *catalog.Graph) error {
	ran := false
	err := e.Store.WithLock(ctx, lockName, func(ctx context.Context) error {
		ran = true
		keys, err := e.keyring(ctx)
		if err != nil {
			return err
		}
		var names []string
		for _, o := range g.Catalog.List(catalog.KindServer) {
			names = append(names, o.Metadata.Name)
		}
		sort.Strings(names)
		var errs []error
		for _, name := range names {
			if err := e.contract(ctx, snap, g, keys, name); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
		return errors.Join(errs...)
	})
	if errors.Is(err, store.ErrLocked) && !ran {
		return nil
	}
	return err
}

func (e *Engine) keyring(ctx context.Context) (*signature.Keyring, error) {
	if e.Keys == nil {
		return signature.NewKeyring(nil), nil
	}
	keys, err := e.Keys(ctx)
	if err != nil {
		return nil, err
	}
	return signature.NewKeyring(keys), nil
}

// contract evaluates one contract.
func (e *Engine) contract(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, keys *signature.Keyring, name string) error {
	q := e.Store.Q()
	st, err := q.ContractState(ctx, name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	p, err := plan.Build(ctx, g, []string{name}, apply.StoreState{S: e.Store})
	if err != nil {
		return q.SetContractSync(ctx, db.SetContractSyncParams{Contract: name, Sync: "unknown", Error: text(err.Error())})
	}
	open, err := q.OpenChange(ctx, name)
	hasOpen := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	if hasOpen && open.Status == Running {
		// A run that ended without its worker closing the change (cancelled
		// before it started, or the worker died): close it now.
		closed, err := e.reconcile(ctx, open)
		if err != nil {
			return err
		}
		if closed {
			return e.contract(ctx, snap, g, keys, name)
		}
	}

	if st.AppliedPlanHash.Valid && st.AppliedPlanHash.String == p.Hash {
		if err := q.SetContractSync(ctx, db.SetContractSyncParams{Contract: name, Sync: "synced"}); err != nil {
			return err
		}
		if hasOpen && open.Status != Running {
			// Git came back to what is applied: nothing to do any more.
			_, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: open.ID, Status: Superseded, FromStatuses: []string{Pending, Approved}})
			return err
		}
		return nil
	}
	if err := q.SetContractSync(ctx, db.SetContractSyncParams{Contract: name, Sync: "out-of-sync"}); err != nil {
		return err
	}

	if hasOpen {
		switch {
		case open.Status == Running:
			return nil
		case open.PlanHash == p.Hash:
			if open.Label != snap.Label() {
				// A newer commit that leaves this contract's plan alone: the
				// change follows it and keeps its approvals.
				commits, by, err := e.commits(ctx, st, snap, keys)
				if err != nil {
					return err
				}
				if err := q.SetChangeSnapshot(ctx, db.SetChangeSnapshotParams{ID: open.ID, Snapshot: mustJSON(snap), Label: snap.Label(), RequestedBy: by, Commits: mustJSON(commits)}); err != nil {
					return err
				}
				open.Snapshot, open.Label, open.RequestedBy, open.Commits = mustJSON(snap), snap.Label(), by, mustJSON(commits)
			}
			return e.advance(ctx, open, g)
		default:
			if _, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: open.ID, Status: Superseded, FromStatuses: []string{Pending, Approved}}); err != nil {
				return err
			}
		}
	}

	// A change that failed, was cancelled or rejected is not tried again
	// with the same plan and commits: a new commit or a retry decides.
	last, err := q.LastChange(ctx, name)
	if err == nil && last.PlanHash == p.Hash && last.Label == snap.Label() && slices.Contains([]string{Failed, Cancelled, Rejected}, last.Status) {
		return nil
	}
	return e.create(ctx, name, "commit", snap, p, st, keys, g)
}

// create opens a change for a contract's new plan and advances it.
func (e *Engine) create(ctx context.Context, name, source string, snap repo.Snapshot, p *plan.Plan, st db.ContractState, keys *signature.Keyring, g *catalog.Graph) error {
	commits, by, err := e.commits(ctx, st, snap, keys)
	if err != nil {
		return err
	}
	c := db.Change{
		ID: store.NewChangeID(), Contract: name, Source: source, Snapshot: mustJSON(snap), Label: snap.Label(),
		PlanHash: p.Hash, Plan: mustJSON(p), Status: Pending, RequestedBy: by, Commits: mustJSON(commits),
	}
	err = e.Store.Q().InsertChange(ctx, db.InsertChangeParams{
		ID: c.ID, Contract: c.Contract, Source: c.Source, Snapshot: c.Snapshot, Label: c.Label,
		PlanHash: c.PlanHash, Plan: c.Plan, Status: c.Status, RequestedBy: c.RequestedBy, Commits: c.Commits,
	})
	if err != nil {
		return err
	}
	e.log().Info("change", "id", c.ID, "contract", name, "plan", p.Hash)
	return e.advance(ctx, c, g)
}

// Commit is a commit a change brings and who signed it.
type Commit struct {
	Repo    string `json:"repo"`
	Commit  string `json:"commit"`
	Signer  string `json:"signer,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// commits checks every commit between what was applied and the snapshot,
// per repository, and returns them with the people who signed them.
func (e *Engine) commits(ctx context.Context, st db.ContractState, snap repo.Snapshot, keys *signature.Keyring) ([]Commit, []string, error) {
	var applied repo.Snapshot
	if len(st.AppliedSnapshot) > 0 {
		_ = json.Unmarshal(st.AppliedSnapshot, &applied)
	}
	from := map[string]string{applied.Platform.URL: applied.Platform.Commit}
	for _, c := range applied.Contracts {
		from[c.URL] = c.Commit
	}
	revs := append([]repo.Rev{snap.Platform}, snap.Contracts...)
	var out []Commit
	seen := map[string]bool{}
	by := []string{}
	for _, rev := range revs {
		if seen[rev.URL+"@"+rev.Commit] {
			continue
		}
		seen[rev.URL+"@"+rev.Commit] = true
		if err := e.Repos.Ensure(ctx, rev.Repo(), rev.Commit); err != nil {
			return nil, nil, err
		}
		g, err := gitsrc.Open(e.Repos.Path(rev.Repo()))
		if err != nil {
			return nil, nil, err
		}
		signed, err := signature.Range(g, from[rev.URL], rev.Commit, keys)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", rev.Name, err)
		}
		for _, s := range signed {
			c := Commit{Repo: rev.Name, Commit: s.Commit, Signer: s.Signer.Username}
			if s.Problem != nil {
				c.Problem = s.Problem.Error()
			} else if !slices.Contains(by, s.Signer.Username) {
				by = append(by, s.Signer.Username)
			}
			out = append(out, c)
		}
	}
	return out, by, nil
}

// Gate is one requirement of a change as the API shows it.
type Gate struct {
	Gate   string          `json:"gate"`
	Status string          `json:"status"`
	Detail json.RawMessage `json:"detail"`
}

// advance works out the change's gates and queues its run once all pass.
func (e *Engine) advance(ctx context.Context, c db.Change, g *catalog.Graph) error {
	if c.Status != Pending && c.Status != Approved {
		return nil
	}
	q := e.Store.Q()
	var p plan.Plan
	if err := json.Unmarshal(c.Plan, &p); err != nil {
		return err
	}
	var commits []Commit
	_ = json.Unmarshal(c.Commits, &commits)

	gates := map[string]Gate{}
	set := func(name, status string, detail any) {
		gates[name] = Gate{Gate: name, Status: status, Detail: mustJSON(detail)}
	}
	set("validation", OK, map[string]string{"plan": c.PlanHash})

	var unsigned []Commit
	for _, cm := range commits {
		if cm.Problem != "" {
			unsigned = append(unsigned, cm)
		}
	}
	if len(unsigned) > 0 {
		set("signature", Blocked, map[string]any{"commits": unsigned})
	} else {
		set("signature", OK, map[string]any{"signers": c.RequestedBy})
	}

	need, err := e.approval(ctx, c, g, &p)
	if err != nil {
		return err
	}
	approved := need.satisfied()
	if len(need.Groups) == 0 {
		set("approval", OK, need)
	} else if approved {
		set("approval", OK, need)
	} else {
		set("approval", Waiting, need)
	}
	for _, gt := range gates {
		if err := q.SetGate(ctx, db.SetGateParams{ChangeID: c.ID, Gate: gt.Gate, Status: gt.Status, Detail: gt.Detail}); err != nil {
			return err
		}
	}
	if gates["signature"].Status != OK || gates["approval"].Status != OK {
		return nil
	}
	if c.Status == Pending && len(need.Groups) > 0 {
		if _, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: c.ID, Status: Approved, FromStatuses: []string{Pending}}); err != nil {
			return err
		}
	}
	return e.start(ctx, c, g, need.approvers())
}

// start queues the change's run. The queue plans again: when the plan is
// no longer the change's, the run is not queued and the next evaluation
// replaces the change.
func (e *Engine) start(ctx context.Context, c db.Change, g *catalog.Graph, approvedBy []string) error {
	var snap repo.Snapshot
	if err := json.Unmarshal(c.Snapshot, &snap); err != nil {
		return err
	}
	by := "gaia"
	if len(c.RequestedBy) > 0 {
		by = c.RequestedBy[0]
	}
	runs, err := queue.EnqueueChange(ctx, e.Store, e.Queue, g, snap, c.Contract, c.PlanHash, c.ID, by, approvedBy)
	if errors.Is(err, queue.ErrPlanChanged) {
		e.log().Info("change waits: the plan changed", "id", c.ID)
		return nil
	}
	if err != nil {
		return err
	}
	e.log().Info("change running", "id", c.ID, "run", runs)
	return nil
}

// reconcile closes a running change whose run has ended, as Finished
// would have; it reports whether it did.
func (e *Engine) reconcile(ctx context.Context, c db.Change) (bool, error) {
	if !c.RunID.Valid {
		return false, nil
	}
	run, err := e.Store.Q().Run(ctx, c.RunID.String)
	if err != nil {
		return false, err
	}
	if run.Status == "queued" || run.Status == "running" {
		return false, nil
	}
	var snap repo.Snapshot
	if err := json.Unmarshal(c.Snapshot, &snap); err != nil {
		return false, err
	}
	var g *catalog.Graph
	if run.Status == "succeeded" {
		if g, err = e.Repos.Graph(ctx, snap); err != nil {
			return false, err
		}
	}
	e.log().Info("change closed after its run", "id", c.ID, "run", run.ID, "status", run.Status)
	return true, e.Finished(ctx, queue.ApplyArgs{Run: run.ID, Snapshot: snap, Contract: c.Contract, ChangeID: c.ID}, g)
}

// Finished closes a change after its run: applied, failed or cancelled.
// An applied change makes the contract synced with the plan taken right
// after the run.
func (e *Engine) Finished(ctx context.Context, a queue.ApplyArgs, g *catalog.Graph) error {
	if a.ChangeID == "" {
		return nil
	}
	q := e.Store.Q()
	run, err := q.Run(ctx, a.Run)
	if err != nil {
		return err
	}
	status := Failed
	switch run.Status {
	case "succeeded":
		status = Applied
	case "cancelled":
		status = Cancelled
	}
	if _, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: a.ChangeID, Status: status, FromStatuses: []string{Running}}); err != nil {
		return err
	}
	if status != Applied || g == nil {
		return nil
	}
	p, err := plan.Build(ctx, g, []string{a.Contract}, apply.StoreState{S: e.Store})
	if err != nil {
		return err
	}
	return q.SetApplied(ctx, db.SetAppliedParams{Contract: a.Contract, AppliedSnapshot: mustJSON(a.Snapshot), AppliedLabel: text(a.Snapshot.Label()), AppliedPlanHash: text(p.Hash)})
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// ErrNotRetryable: only a failed or cancelled change, whose plan is still
// the contract's, can be tried again.
var ErrNotRetryable = errors.New("only a failed or cancelled change with the current plan can be retried")

// Retry opens a new change with the same plan as a failed or cancelled
// one, at the current snapshot; its requirements are checked again.
func (e *Engine) Retry(ctx context.Context, id string, snap repo.Snapshot, g *catalog.Graph) (string, error) {
	q := e.Store.Q()
	c, err := q.Change(ctx, id)
	if err != nil {
		return "", err
	}
	if c.Status != Failed && c.Status != Cancelled {
		return "", ErrNotRetryable
	}
	if _, err := q.OpenChange(ctx, c.Contract); err == nil {
		return "", ErrNotRetryable
	}
	p, err := plan.Build(ctx, g, []string{c.Contract}, apply.StoreState{S: e.Store})
	if err != nil {
		return "", err
	}
	if p.Hash != c.PlanHash {
		return "", ErrNotRetryable
	}
	st, err := q.ContractState(ctx, c.Contract)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	keys, err := e.keyring(ctx)
	if err != nil {
		return "", err
	}
	if err := e.create(ctx, c.Contract, "retry", snap, p, st, keys, g); err != nil {
		return "", err
	}
	open, err := q.OpenChange(ctx, c.Contract)
	if err != nil {
		// It already ran to the end.
		last, lerr := q.LastChange(ctx, c.Contract)
		return last.ID, lerr
	}
	return open.ID, nil
}
