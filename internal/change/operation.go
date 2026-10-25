package change

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Expired: an approved Operation did not start in time.
const Expired = "expired"

// OperationKey is the change key of an Operation: its changes live
// beside contracts' (design 6.4).
func OperationKey(name string) string { return catalog.KindOperation + "/" + name }

// specHash is an Operation's spec as written, to keep it from changing
// once approved.
func specHash(o *catalog.Object) string {
	b, _ := json.Marshal(o.SpecMap())
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// operation brings an Operation's change in line (design 6.4): one change
// to approve its plan, then it runs once. An Operation that ran, or is
// scheduled, is not planned again; a commit that changes it is refused.
func (e *Engine) operation(ctx context.Context, snap repo.Snapshot, g *catalog.Graph, keys *signature.Keyring, o *catalog.Object) error {
	q := e.Store.Q()
	name, key := o.Metadata.Name, OperationKey(o.Metadata.Name)
	refuse := func(msg string) error {
		return q.SetContractSync(ctx, db.SetContractSyncParams{Contract: key, Sync: "unknown", Error: text(msg)})
	}
	row, err := q.Operation(ctx, name)
	switch {
	case err == nil:
		if row.SpecHash != specHash(o) {
			return refuse(fmt.Sprintf("Operation %s was approved as it was (%s); it does not change. Write a new Operation.", name, row.Status))
		}
		return q.SetContractSync(ctx, db.SetContractSyncParams{Contract: key, Sync: "synced"})
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	op, err := plan.BuildOperation(ctx, g, name, apply.StoreState{S: e.Store})
	if err != nil {
		return refuse(err.Error())
	}
	open, err := q.OpenChange(ctx, key)
	hasOpen := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	problem := ""
	switch {
	case len(op.Targets) == 0:
		problem = "the selector takes no active server"
	case op.NotAfter != nil && time.Now().After(*op.NotAfter):
		problem = "its notAfter has passed"
	case op.Window != nil && time.Now().After(op.Window.End):
		problem = "its window has passed"
	}
	if pol := g.Policy(); problem == "" && pol != nil && op.Window == nil {
		if ops := pol.Spec.(*catalog.PolicySpec).Operations; ops != nil {
			for _, env := range op.Envs {
				if slices.Contains(ops.RequireWindow[env], op.Action) {
					problem = fmt.Sprintf("%s in %s needs a window", op.Action, env)
				}
			}
		}
	}
	if problem != "" {
		if hasOpen && open.Status != Running {
			if _, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: open.ID, Status: Superseded, FromStatuses: []string{Pending, Approved}}); err != nil {
				return err
			}
		}
		return refuse(problem)
	}
	if err := q.SetContractSync(ctx, db.SetContractSyncParams{Contract: key, Sync: "out-of-sync"}); err != nil {
		return err
	}
	if hasOpen {
		switch {
		case open.Status == Running:
			return nil
		case open.PlanHash == op.Hash:
			return e.advanceOperation(ctx, open, g, op, snap, specHash(o))
		default:
			if _, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: open.ID, Status: Superseded, FromStatuses: []string{Pending, Approved}}); err != nil {
				return err
			}
		}
	}
	last, err := q.LastChange(ctx, key)
	if err == nil && last.PlanHash == op.Hash && slices.Contains([]string{Failed, Cancelled, Rejected, Expired}, last.Status) {
		return nil
	}
	commits, by, err := e.commits(ctx, db.ContractState{}, snap, keys)
	if err != nil {
		return err
	}
	c := db.Change{ID: store.NewChangeID(), Contract: key, Source: "operation", Snapshot: mustJSON(snap), Label: snap.Label(),
		PlanHash: op.Hash, Plan: mustJSON(op), Status: Pending, RequestedBy: by, Commits: mustJSON(commits)}
	if err := q.InsertChange(ctx, db.InsertChangeParams{ID: c.ID, Contract: c.Contract, Source: c.Source, Snapshot: c.Snapshot, Label: c.Label,
		PlanHash: c.PlanHash, Plan: c.Plan, Status: c.Status, RequestedBy: c.RequestedBy, Commits: c.Commits}); err != nil {
		return err
	}
	e.log().Info("operation change", "id", c.ID, "operation", name, "servers", len(op.Targets))
	return e.advanceOperation(ctx, c, g, op, snap, specHash(o))
}

// advanceOperation works out an Operation change's gates and schedules
// it once they pass.
func (e *Engine) advanceOperation(ctx context.Context, c db.Change, g *catalog.Graph, op *plan.Operation, snap repo.Snapshot, spec string) error {
	if c.Status != Pending && c.Status != Approved {
		return nil
	}
	q := e.Store.Q()
	var commits []Commit
	_ = json.Unmarshal(c.Commits, &commits)
	gates := map[string]Gate{}
	set := func(name, status string, detail any) {
		gates[name] = Gate{Gate: name, Status: status, Detail: mustJSON(detail)}
	}
	set("validation", OK, map[string]any{"plan": c.PlanHash, "servers": len(op.Targets)})
	unsigned := slices.DeleteFunc(append([]Commit(nil), commits...), func(cm Commit) bool { return cm.Problem == "" })
	if len(unsigned) > 0 {
		set("signature", Blocked, map[string]any{"commits": unsigned})
	} else {
		set("signature", OK, map[string]any{"signers": c.RequestedBy})
	}
	need, err := e.operationApproval(ctx, c, g, op)
	if err != nil {
		return err
	}
	status := OK
	if !need.satisfied() {
		status = Waiting
	}
	set("approval", status, need)
	window := map[string]any{}
	if op.Window != nil {
		window["start"], window["end"] = op.Window.Start, op.Window.End
	}
	set("window", OK, window)
	for _, gt := range gates {
		if err := q.SetGate(ctx, db.SetGateParams{ChangeID: c.ID, Gate: gt.Gate, Status: gt.Status, Detail: gt.Detail}); err != nil {
			return err
		}
	}
	if gates["approval"].Status == Waiting && gates["signature"].Status == OK {
		e.emit(ctx, g, "change.pending", c, func(ev *notify.Event) {
			for _, gr := range need.Groups {
				if gr.ApprovedBy == "" {
					ev.Approvers = append(ev.Approvers, notify.Approver{Name: gr.Name, Teams: gr.Teams, Candidates: gr.Candidates, Why: gr.Why})
				}
			}
			ev.Teams = op.Owners
		})
	}
	if gates["signature"].Status != OK || gates["approval"].Status != OK {
		return nil
	}
	if n, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: c.ID, Status: Approved, FromStatuses: []string{Pending}}); err != nil {
		return err
	} else if n == 1 {
		c.Status = Approved
		e.emit(ctx, g, "change.approved", c, func(ev *notify.Event) { ev.ApprovedBy, ev.Teams = need.approvers(), op.Owners })
	}
	by := "gaia"
	if len(c.RequestedBy) > 0 {
		by = c.RequestedBy[0]
	}
	if e.Queue == nil {
		return nil
	}
	err = queue.EnqueueOperation(ctx, e.Store, e.Queue, op, queue.OperationArgs{
		Name: op.Name, ChangeID: c.ID, Snapshot: snap, RequestedBy: by, ApprovedBy: need.approvers(),
	}, spec)
	if err != nil {
		return err
	}
	e.log().Info("operation scheduled", "operation", op.Name, "change", c.ID)
	return nil
}

// operationApproval is who approves an Operation (design 6.4): the roles
// Policy.operations gives its action in each of its environments. The
// owner's part falls to multiOwner (platform-admin) when it touches
// several owners' contracts.
func (e *Engine) operationApproval(ctx context.Context, c db.Change, g *catalog.Graph, op *plan.Operation) (Need, error) {
	need := Need{Excluded: append([]string{}, c.RequestedBy...)}
	if strings.HasPrefix(c.Contract, catalog.KindOperation+"/") {
		if by, ok := e.approvedBySchedule(ctx, g, op.Name); ok {
			need.Via = ScheduleKey(by)
			return need, nil
		}
	}
	var ops *catalog.OperationPolicy
	var roles map[string][]string
	if pol := g.Policy(); pol != nil {
		ps := pol.Spec.(*catalog.PolicySpec)
		ops, roles = ps.Operations, ps.GaiaRoles
	}
	envs := op.Envs
	if len(envs) == 0 {
		envs = []string{"default"}
	}
	want := map[string]bool{}
	for _, env := range envs {
		for _, r := range ops.Approvers(env, op.Action) {
			want[r] = true
		}
	}
	admin := func(why string) {
		if i := slices.IndexFunc(need.Groups, func(gr Group) bool { return gr.Name == "platform-admin" }); i >= 0 {
			need.Groups[i].Why += "; " + why
			return
		}
		need.Groups = append(need.Groups, Group{Name: "platform-admin", Teams: roles["platform-admin"], Role: "member", Why: why})
	}
	if want["owner"] {
		switch len(op.Owners) {
		case 0:
		case 1:
			gr := Group{Name: "owner", Teams: op.Owners, Role: "member", Why: op.Action + " on " + op.Owners[0] + "'s servers"}
			if t := g.Team(op.Owners[0]); t != nil && t.ApproversGroup != nil {
				gr.Role = "approver"
			}
			need.Groups = append(need.Groups, gr)
		default:
			admin("it touches several owners' servers (" + strings.Join(op.Owners, ", ") + ")")
		}
	}
	if want["platform-admin"] {
		admin("Policy: " + op.Action + " in " + strings.Join(envs, ", "))
	}
	return need, e.fill(ctx, c, &need)
}

// OperationDone closes an Operation's change after it ran, or expired.
func (e *Engine) OperationDone(ctx context.Context, a queue.OperationArgs, status string) error {
	cs := map[string]string{"succeeded": Applied, "partial": Failed, "failed": Failed, "cancelled": Cancelled, "expired": Expired}[status]
	if cs == "" {
		cs = Failed
	}
	q := e.Store.Q()
	n, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: a.ChangeID, Status: cs, FromStatuses: []string{Running}})
	if err != nil || n != 1 {
		return err
	}
	c, err := q.Change(ctx, a.ChangeID)
	if err != nil {
		return err
	}
	var op plan.Operation
	_ = json.Unmarshal(c.Plan, &op)
	trigger := "change.applied"
	if cs != Applied {
		trigger = "change.failed"
	}
	e.emit(ctx, nil, trigger, c, func(ev *notify.Event) {
		ev.Teams, ev.ApprovedBy = op.Owners, a.ApprovedBy
		if status != "succeeded" {
			ev.Error = "Operation " + status
		}
	})
	return nil
}
