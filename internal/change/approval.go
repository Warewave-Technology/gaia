package change

import (
	"context"
	"slices"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Need is what the approval gate waits for: one approval from each group,
// by someone who did not sign the change's commits.
type Need struct {
	Groups []Group `json:"groups"`
	// Excluded are the change's signers: they may not approve it.
	Excluded []string `json:"excluded,omitempty"`
}

// Group is one approval the change needs.
type Group struct {
	Name       string   `json:"name"` // owner | platform-admin
	Teams      []string `json:"teams"`
	Candidates []string `json:"candidates"`
	ApprovedBy string   `json:"approvedBy,omitempty"`
	Why        string   `json:"why"`
}

func (n Need) satisfied() bool {
	for _, g := range n.Groups {
		if g.ApprovedBy == "" {
			return false
		}
	}
	return true
}

// approval works out who must approve a change (design 6.4, 9.9) and who
// already did. Rules come from the Policy:
//   - an environment with approval: required needs the owner's approval
//   - deleting servers where deleteProtection is on also needs a
//     platform-admin
//
// Approvals count only for the change's plan hash.
func (e *Engine) approval(ctx context.Context, c db.Change, g *catalog.Graph, p *plan.Plan) (Need, error) {
	need := Need{Excluded: c.RequestedBy}
	contract, ok := g.Catalog.Get(catalog.KindServer, c.Contract)
	if !ok {
		return need, nil
	}
	env := contract.Metadata.Labels["env"]
	var ep catalog.EnvironmentPolicy
	var roles map[string][]string
	if pol := g.Policy(); pol != nil {
		ps := pol.Spec.(*catalog.PolicySpec)
		ep, roles = ps.Environments[env], ps.GaiaRoles
	}
	deletes := false
	for _, ct := range p.Contracts {
		for _, s := range ct.Servers {
			deletes = deletes || s.Action == plan.Delete
		}
	}
	owner := contract.Metadata.Labels["owner"]
	if ep.Approval == "required" || deletes && ep.DeleteProtection {
		why := "env " + env + " needs approval"
		if ep.Approval != "required" {
			why = "deleting servers in env " + env
		}
		need.Groups = append(need.Groups, Group{Name: "owner", Teams: []string{owner}, Why: why})
	}
	if deletes && ep.DeleteProtection {
		need.Groups = append(need.Groups, Group{Name: "platform-admin", Teams: roles["platform-admin"], Why: "deleting servers in env " + env})
	}
	if len(need.Groups) == 0 {
		return need, nil
	}

	q := e.Store.Q()
	approvals, err := q.Approvals(ctx, c.ID)
	if err != nil {
		return need, err
	}
	for i := range need.Groups {
		gr := &need.Groups[i]
		members, err := q.TeamMembers(ctx, gr.Teams)
		if err != nil {
			return need, err
		}
		gr.Candidates = []string{}
		for _, m := range members {
			if !slices.Contains(c.RequestedBy, m.Username) {
				gr.Candidates = append(gr.Candidates, m.Username)
			}
		}
		for _, a := range approvals {
			if a.Decision != "approve" || a.PlanHash != c.PlanHash || !slices.Contains(gr.Candidates, a.Username) {
				continue
			}
			// One person fills one group: owner and platform-admin are two
			// people.
			taken := false
			for _, other := range need.Groups {
				taken = taken || other.ApprovedBy == a.Username
			}
			if !taken {
				gr.ApprovedBy = a.Username
				break
			}
		}
	}
	return need, nil
}
