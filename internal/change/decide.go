package change

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/notify"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Errors a decision is refused with.
var (
	ErrClosed      = errors.New("the change is no longer open for decisions")
	ErrStalePlan   = errors.New("the change's plan is not the one you saw; look again")
	ErrNotApprover = errors.New("you are not among the people who may decide on this change")
	ErrNoApproval  = errors.New("this change needs no approval")
	ErrDecided     = errors.New("you already decided on this plan")
)

// Decision is someone's approval or rejection of a change, for the plan
// they saw.
type Decision struct {
	Change   string
	PlanHash string
	UserID   string
	Username string
	Approve  bool
	Comment  string
}

// Decide records a decision (design 7.6). Only who the approval gate
// lists may decide, and only for the change's current plan. A rejection
// closes the change; an approval may complete the gate and start the run.
func (e *Engine) Decide(ctx context.Context, d Decision) error {
	return e.Store.WaitLock(ctx, lockName, func(ctx context.Context) error {
		q := e.Store.Q()
		c, err := q.Change(ctx, d.Change)
		if err != nil {
			return err
		}
		if c.Status != Pending && c.Status != Approved {
			return ErrClosed
		}
		if d.PlanHash != c.PlanHash {
			return ErrStalePlan
		}
		var snap repo.Snapshot
		if err := json.Unmarshal(c.Snapshot, &snap); err != nil {
			return err
		}
		// The change is judged by the snapshot it came from.
		g, err := e.Repos.Graph(ctx, snap)
		if err != nil {
			return err
		}
		var need Need
		var op *plan.Operation
		if strings.HasPrefix(c.Contract, catalog.KindOperation+"/") {
			op = &plan.Operation{}
			if err := json.Unmarshal(c.Plan, op); err != nil {
				return err
			}
			need, err = e.operationApproval(ctx, c, g, op)
		} else {
			var p plan.Plan
			if err := json.Unmarshal(c.Plan, &p); err != nil {
				return err
			}
			need, err = e.approval(ctx, c, g, &p)
		}
		if err != nil {
			return err
		}
		switch {
		case len(need.Groups) == 0:
			return ErrNoApproval
		case !need.may(d.Username):
			return ErrNotApprover
		}
		previous, err := q.Approvals(ctx, c.ID)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(previous, func(a db.Approval) bool { return a.Username == d.Username && a.PlanHash == c.PlanHash }) {
			return ErrDecided
		}
		decision := "reject"
		if d.Approve {
			decision = "approve"
		}
		if err := q.InsertApproval(ctx, db.InsertApprovalParams{
			ChangeID: c.ID, PlanHash: c.PlanHash, UserID: d.UserID, Username: d.Username, Decision: decision, Comment: text(d.Comment),
		}); err != nil {
			return err
		}
		e.log().Info("change decided", "id", c.ID, "by", d.Username, "decision", decision)
		if !d.Approve {
			if _, err := q.SetChangeStatus(ctx, db.SetChangeStatusParams{ID: c.ID, Status: Rejected, FromStatuses: []string{Pending, Approved}}); err != nil {
				return err
			}
			c.Status = Rejected
			e.emit(ctx, g, "change.rejected", c, func(ev *notify.Event) { ev.DecidedBy, ev.Comment = d.Username, d.Comment })
			return nil
		}
		if op != nil {
			o, ok := g.Catalog.Get(catalog.KindOperation, op.Name)
			if !ok {
				return fmt.Errorf("no Operation %s at the change's commit", op.Name)
			}
			return e.advanceOperation(ctx, c, g, op, snap, specHash(o))
		}
		return e.advance(ctx, c, g)
	})
}
