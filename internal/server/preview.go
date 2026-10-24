package server

import (
	"errors"
	"net/http"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// Preview is what a ref that Gaia does not follow would change.
type Preview struct {
	Repo      string            `json:"repo"`
	Ref       string            `json:"ref"`
	Commit    string            `json:"commit"`
	Label     string            `json:"label"`
	Contracts []PreviewContract `json:"contracts"`
	// Error is why the ref does not load or plan: what validation says.
	Error string `json:"error,omitempty"`
}

// PreviewContract is one contract's plan at the ref against what is
// applied.
type PreviewContract struct {
	Name     string `json:"name"`
	Changed  bool   `json:"changed"`
	PlanHash string `json:"planHash,omitempty"`
	Applied  string `json:"appliedPlanHash,omitempty"`
	// Plan is the contract's plan, for those who see it in full.
	Plan *plan.Contract `json:"plan,omitempty"`
	// Removed: the contract is applied now but the ref no longer has it.
	Removed    bool `json:"removed,omitempty"`
	Restricted bool `json:"restricted,omitempty"`
}

// preview plans a branch, tag, pull request or commit of a followed
// repository (design 7.6): the other repositories stay at their heads.
// Nothing is applied and no change is opened.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Catalog == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server does not follow a repository"})
		return
	}
	ctx := r.Context()
	name, ref := r.URL.Query().Get("repo"), r.URL.Query().Get("ref")
	if name == "" {
		name = "platform"
	}
	if ref == "" {
		badRequest(w, errors.New("say which ref to preview (ref)"))
		return
	}
	snap, err := s.cfg.Catalog.Snapshot()
	if err != nil && snap.Platform.Commit == "" {
		s.fail(w, err)
		return
	}
	out := Preview{Repo: name, Ref: ref, Contracts: []PreviewContract{}}
	var rev *repo.Rev
	if name == snap.Platform.Name {
		rev = &snap.Platform
	}
	for i := range snap.Contracts {
		if snap.Contracts[i].Name == name {
			rev = &snap.Contracts[i]
		}
	}
	if rev == nil {
		writeJSON(w, http.StatusNotFound, Problem{Error: "no followed platform or contract repository is named " + name})
		return
	}
	commit, err := s.cfg.Repos.Fetch(ctx, rev.Repo(), ref)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, Problem{Error: err.Error()})
		return
	}
	out.Commit = commit
	if rev == &snap.Platform {
		// The platform repository says where everything else is.
		p := snap.Platform
		p.Commit = commit
		if snap, err = s.cfg.Repos.At(ctx, p); err != nil {
			out.Error = err.Error()
			writeJSON(w, http.StatusOK, out)
			return
		}
	} else {
		rev.Commit = commit
	}
	out.Label = snap.Label()
	g, err := s.cfg.Repos.Graph(ctx, snap)
	if err != nil {
		out.Error = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	q := s.store.Q()
	seen := map[string]bool{}
	for _, o := range g.Catalog.List(catalog.KindServer) {
		name := o.Metadata.Name
		seen[name] = true
		p, err := plan.Build(ctx, g, []string{name}, apply.StoreState{S: s.store})
		if err != nil {
			out.Error = err.Error()
			out.Contracts = []PreviewContract{}
			break
		}
		pc := PreviewContract{Name: name, PlanHash: p.Hash, Plan: &p.Contracts[0]}
		if st, err := q.ContractState(ctx, name); err == nil {
			pc.Applied = st.AppliedPlanHash.String
		}
		pc.Changed = pc.Applied != pc.PlanHash
		out.Contracts = append(out.Contracts, pc)
	}
	// Contracts applied now that the ref no longer has.
	states, err := q.ContractStates(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, st := range states {
		if !seen[st.Contract] && st.AppliedPlanHash.Valid {
			out.Contracts = append(out.Contracts, PreviewContract{Name: st.Contract, Changed: true, Removed: true, Applied: st.AppliedPlanHash.String})
		}
	}
	for i, pc := range out.Contracts {
		if !s.full(r, []string{pc.Name}) {
			out.Contracts[i] = PreviewContract{Name: pc.Name, Changed: pc.Changed, Removed: pc.Removed, Restricted: true}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
