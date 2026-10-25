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
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Need is what the approval gate waits for: one approval from each group,
// by someone who did not sign the change's commits.
type Need struct {
	Groups []Group `json:"groups"`
	// Via: approved another way, such as by an OperationSchedule
	// approved once.
	Via string `json:"via,omitempty"`
	// Excluded may not approve it: its signers, and the members of the
	// teams it gives access to.
	Excluded []string `json:"excluded,omitempty"`
}

// Group is one approval the change needs.
type Group struct {
	Name  string   `json:"name"` // owner | platform-admin
	Teams []string `json:"teams"`
	// Role is who of the teams approve: their members, or the people of
	// the team's approvers group (design 14.1).
	Role       string   `json:"role"`
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

func (n Need) approvers() []string {
	out := []string{}
	for _, g := range n.Groups {
		if g.ApprovedBy != "" {
			out = append(out, g.ApprovedBy)
		}
	}
	return out
}

// may reports whether username may decide on the change.
func (n Need) may(username string) bool {
	for _, g := range n.Groups {
		if slices.Contains(g.Candidates, username) {
			return true
		}
	}
	return false
}

// approval works out who must approve a change (design 6.4, 9.9) and who
// already did. Rules come from the Policy:
//   - an environment with approval: required needs the owner's approval
//   - deleting servers where deleteProtection is on also needs a
//     platform-admin
//
// Approvals count only for the change's plan hash.
func (e *Engine) approval(ctx context.Context, c db.Change, g *catalog.Graph, p *plan.Plan) (Need, error) {
	need := Need{Excluded: append([]string{}, c.RequestedBy...)}
	contract, ok := g.Catalog.Get(catalog.KindServer, c.Contract)
	if !ok || c.Source == DriftAuto {
		// Putting drift right at once was approved with the plan.
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
	if ep.Approval == "required" || deletes && ep.DeleteProtection || c.Source == Drift {
		why := "env " + env + " needs approval"
		switch {
		case c.Source == Drift:
			why = "puts drift right: the systems are set back to the applied plan"
		case ep.Approval != "required":
			why = "deleting servers in env " + env
		}
		gr := Group{Name: "owner", Teams: []string{owner}, Role: "member", Why: why}
		if t := g.Team(owner); t != nil && t.ApproversGroup != nil {
			gr.Role = "approver"
		}
		others, err := e.otherOwners(ctx, c, g, owner)
		if err != nil {
			return need, err
		}
		if len(others) > 0 {
			// A commit that changes several owners' contracts: a
			// platform-admin may approve for the owner (design 7.6).
			// The platform admins' teams approve by membership; when the
			// owner's team has approvers, they stay the owner's.
			gr.Teams = append(gr.Teams, roles["platform-admin"]...)
			gr.Why += "; the same commits change " + strings.Join(others, ", ") + ", so a platform-admin may approve too"
		}
		need.Groups = append(need.Groups, gr)
	}
	if deletes && ep.DeleteProtection {
		need.Groups = append(need.Groups, Group{Name: "platform-admin", Teams: roles["platform-admin"], Role: "member", Why: "deleting servers in env " + env})
	}

	// Access the change grants (design 14.3): through a privileged group
	// it needs a platform admin; through another team's group, that
	// team; and nobody approves access for a team they are in.
	q := e.Store.Q()
	granted, err := e.granted(ctx, c.Contract, p)
	if err != nil {
		return need, err
	}
	for _, a := range granted {
		group := strings.TrimPrefix(a.Via, "group/")
		if a.Privileged {
			why := "access through privileged group " + group
			if i := slices.IndexFunc(need.Groups, func(gr Group) bool { return gr.Name == "platform-admin" }); i >= 0 {
				need.Groups[i].Why += "; " + why
			} else {
				need.Groups = append(need.Groups, Group{Name: "platform-admin", Teams: roles["platform-admin"], Role: "member", Why: why})
			}
		}
		if a.Owner != "" && a.Owner != owner && !slices.ContainsFunc(need.Groups, func(gr Group) bool { return gr.Name == "group-owner/"+group }) {
			gr := Group{Name: "group-owner/" + group, Teams: []string{a.Owner}, Role: "member", Why: "access through group " + group + ", which " + a.Owner + " owns"}
			if t := g.Team(a.Owner); t != nil && t.ApproversGroup != nil {
				gr.Role = "approver"
			}
			need.Groups = append(need.Groups, gr)
		}
		gainers, err := q.TeamMembers(ctx, db.TeamMembersParams{Teams: a.Teams, Role: "member"})
		if err != nil {
			return need, err
		}
		for _, m := range gainers {
			if !slices.Contains(need.Excluded, m.Username) {
				need.Excluded = append(need.Excluded, m.Username)
			}
		}
	}
	if len(need.Groups) == 0 {
		return need, nil
	}

	return need, e.fill(ctx, c, &need)
}

// fill finds each group's candidates (its teams' people, less the
// excluded) and who of them approved the change's plan.
func (e *Engine) fill(ctx context.Context, c db.Change, need *Need) error {
	q := e.Store.Q()
	approvals, err := q.Approvals(ctx, c.ID)
	if err != nil {
		return err
	}
	for i := range need.Groups {
		gr := &need.Groups[i]
		gr.Candidates = []string{}
		if len(gr.Teams) == 0 {
			continue // nobody may approve: the change waits, and says so
		}
		members, err := q.TeamMembers(ctx, db.TeamMembersParams{Teams: gr.Teams[:1], Role: gr.Role})
		if err != nil {
			return err
		}
		if len(gr.Teams) > 1 {
			// Further teams (platform admins for a commit across owners)
			// approve by membership.
			more, err := q.TeamMembers(ctx, db.TeamMembersParams{Teams: gr.Teams[1:], Role: "member"})
			if err != nil {
				return err
			}
			members = append(members, more...)
		}
		for _, m := range members {
			if !slices.Contains(need.Excluded, m.Username) && !slices.Contains(gr.Candidates, m.Username) {
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
	return nil
}

// otherOwners returns the owners, other than owner, of the open changes
// that bring any of c's commits.
func (e *Engine) otherOwners(ctx context.Context, c db.Change, g *catalog.Graph, owner string) ([]string, error) {
	var mine []Commit
	_ = json.Unmarshal(c.Commits, &mine)
	set := map[string]bool{}
	for _, cm := range mine {
		set[cm.Repo+"@"+cm.Commit] = true
	}
	open, err := e.Store.Q().OpenChanges(ctx)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, o := range open {
		if o.ID == c.ID {
			continue
		}
		var theirs []Commit
		_ = json.Unmarshal(o.Commits, &theirs)
		shared := slices.ContainsFunc(theirs, func(cm Commit) bool { return set[cm.Repo+"@"+cm.Commit] })
		if !shared {
			continue
		}
		if other, ok := g.Catalog.Get(catalog.KindServer, o.Contract); ok {
			if ow := other.Metadata.Labels["owner"]; ow != "" && ow != owner {
				found[ow] = true
			}
		}
	}
	out := make([]string, 0, len(found))
	for ow := range found {
		out = append(out, ow)
	}
	sort.Strings(out)
	return out, nil
}

// granted returns the access entries of the change's plan that are new
// or different from the plan last applied: what the change grants.
func (e *Engine) granted(ctx context.Context, contract string, p *plan.Plan) ([]plan.Access, error) {
	var now []plan.Access
	for _, c := range p.Contracts {
		if c.Name == contract {
			now = c.Access
		}
	}
	if len(now) == 0 {
		return nil, nil
	}
	before := map[string]plan.Access{}
	st, err := e.Store.Q().ContractState(ctx, contract)
	if err == nil && len(st.AppliedPlan) > 0 {
		var ap plan.Plan
		if json.Unmarshal(st.AppliedPlan, &ap) == nil {
			for _, c := range ap.Contracts {
				if c.Name == contract {
					for _, a := range c.Access {
						before[a.Via] = a
					}
				}
			}
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var out []plan.Access
	for _, a := range now {
		b, ok := before[a.Via]
		if !ok || b.Sudo != a.Sudo && a.Sudo || !subset(a.Teams, b.Teams) {
			out = append(out, a)
		}
	}
	return out, nil
}

// subset reports whether every element of a is in b.
func subset(a, b []string) bool {
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}
