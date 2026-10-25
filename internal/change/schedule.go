package change

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/cel"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// ScheduleKey is the change key of an OperationSchedule approved once.
func ScheduleKey(name string) string { return catalog.KindSchedule + "/" + name }

// schedulePlan is what approving a schedule approves: its spec, and who
// it touches now.
type schedulePlan struct {
	Schedule string         `json:"schedule"`
	Spec     map[string]any `json:"spec"`
	Action   string         `json:"action"`
	Owners   []string       `json:"owners"`
	Envs     []string       `json:"envs"`
}

// schedule keeps the change of an OperationSchedule with approval: once
// (design 6.4): approving it approves the Operations it writes, for as
// long as it stays as approved.
func (e *Engine) schedule(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, keys *signature.Keyring, o *catalog.Object) error {
	spec := o.Spec.(*catalog.OperationScheduleSpec)
	if spec.Approval != "once" {
		return nil
	}
	q := e.Store.Q()
	key, hash := ScheduleKey(o.Metadata.Name), specHash(o)
	st, err := q.ContractState(ctx, key)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if st.AppliedPlanHash.String == hash {
		return q.SetContractSync(ctx, db.SetContractSyncParams{Contract: key, Sync: "synced"})
	}
	if err := q.SetContractSync(ctx, db.SetContractSyncParams{Contract: key, Sync: "out-of-sync"}); err != nil {
		return err
	}
	open, err := q.OpenChange(ctx, key)
	if err == nil {
		if open.PlanHash == hash {
			return e.advanceSchedule(ctx, open, g, snap)
		}
		if _, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: open.ID, Status: Superseded, FromStatuses: []string{Pending, Approved}}); err != nil {
			return err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	sp, err := touches(g, o.Metadata.Name, spec)
	if err != nil {
		return q.SetContractSync(ctx, db.SetContractSyncParams{Contract: key, Sync: "unknown", Error: text(err.Error())})
	}
	sp.Spec = o.SpecMap()
	commits, by, err := e.commits(ctx, db.ContractState{}, snap, keys)
	if err != nil {
		return err
	}
	c := db.Change{ID: store.NewChangeID(), Contract: key, Source: "schedule", Snapshot: mustJSON(snap), Label: snap.Label(),
		PlanHash: hash, Plan: mustJSON(sp), Status: Pending, RequestedBy: by, Commits: mustJSON(commits)}
	if err := q.InsertChange(ctx, db.InsertChangeParams{ID: c.ID, Contract: c.Contract, Source: c.Source, Snapshot: c.Snapshot, Label: c.Label,
		PlanHash: c.PlanHash, Plan: c.Plan, Status: c.Status, RequestedBy: c.RequestedBy, Commits: c.Commits}); err != nil {
		return err
	}
	return e.advanceSchedule(ctx, c, g, snap)
}

// touches is who a schedule's selector reaches now: the owners and
// environments of the contracts it takes.
func touches(g *catalog.Graph, name string, spec *catalog.OperationScheduleSpec) (schedulePlan, error) {
	sp := schedulePlan{Schedule: name, Action: spec.Template.Action, Owners: []string{}, Envs: []string{}}
	sel, err := cel.Selector(spec.Template.Selector)
	if err != nil {
		return sp, err
	}
	for _, c := range g.Catalog.List(catalog.KindServer) {
		labels := map[string]string{}
		for k, v := range c.Metadata.Labels {
			labels[k] = v
		}
		ok, err := sel.EvalBool(map[string]any{"contract": c.Metadata.Name, "labels": labels, "env": labels["env"], "owner": labels["owner"], "spec": c.SpecMap()})
		if err != nil {
			return sp, err
		}
		if !ok {
			continue
		}
		if o := labels["owner"]; o != "" && !slices.Contains(sp.Owners, o) {
			sp.Owners = append(sp.Owners, o)
		}
		if env := labels["env"]; env != "" && !slices.Contains(sp.Envs, env) {
			sp.Envs = append(sp.Envs, env)
		}
	}
	sort.Strings(sp.Owners)
	sort.Strings(sp.Envs)
	return sp, nil
}

// advanceSchedule approves a schedule's change once its approvals are in;
// nothing runs, the schedule is from then on approved as it is.
func (e *Engine) advanceSchedule(ctx context.Context, c db.Change, g *catalog.Graph, snap repo.Snapshot) error {
	if c.Status != Pending {
		return nil
	}
	q := e.Store.Q()
	var sp schedulePlan
	if err := json.Unmarshal(c.Plan, &sp); err != nil {
		return err
	}
	need, err := e.operationApproval(ctx, c, g, &plan.Operation{Name: sp.Schedule, Action: sp.Action, Owners: sp.Owners, Envs: sp.Envs})
	if err != nil {
		return err
	}
	status := OK
	if !need.satisfied() {
		status = Waiting
	}
	if err := q.SetGate(ctx, db.SetGateParams{ChangeID: c.ID, Gate: "approval", Status: status, Detail: mustJSON(need)}); err != nil {
		return err
	}
	if status == Waiting {
		e.emit(ctx, g, "change.pending", c, func(ev *notify.Event) {
			for _, gr := range need.Groups {
				if gr.ApprovedBy == "" {
					ev.Approvers = append(ev.Approvers, notify.Approver{Name: gr.Name, Teams: gr.Teams, Candidates: gr.Candidates, Why: gr.Why})
				}
			}
			ev.Teams = sp.Owners
		})
		return nil
	}
	if err := q.SetApplied(ctx, db.SetAppliedParams{Contract: c.Contract, AppliedSnapshot: c.Snapshot, AppliedLabel: text(c.Label), AppliedPlanHash: text(c.PlanHash), AppliedPlan: c.Plan}); err != nil {
		return err
	}
	if n, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: c.ID, Status: Applied, FromStatuses: []string{Pending}}); err != nil || n != 1 {
		return err
	}
	e.emit(ctx, g, "change.applied", c, func(ev *notify.Event) { ev.ApprovedBy, ev.Teams = need.approvers(), sp.Owners })
	return nil
}

// approvedBySchedule reports whether an Operation was written by a
// schedule approved once, as it is now.
func (e *Engine) approvedBySchedule(ctx context.Context, g *catalog.Graph, name string) (string, bool) {
	o, ok := g.Catalog.Get(catalog.KindOperation, name)
	if !ok {
		return "", false
	}
	by, ok := strings.CutPrefix(o.Metadata.Annotations[catalog.AnnotationGeneratedBy], catalog.KindSchedule+"/")
	if !ok {
		return "", false
	}
	so, ok := g.Catalog.Get(catalog.KindSchedule, by)
	if !ok || so.Spec.(*catalog.OperationScheduleSpec).Approval != "once" {
		return "", false
	}
	st, err := e.Store.Q().ContractState(ctx, ScheduleKey(by))
	return by, err == nil && st.AppliedPlanHash.String == specHash(so)
}
