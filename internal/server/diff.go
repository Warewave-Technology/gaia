package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/pmezard/go-difflib/difflib"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// ObjectDiff is how one object in Git differs between two snapshots.
type ObjectDiff struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// File is where it is written at the newer snapshot (the older one
	// for a removed object).
	File   string `json:"file"`
	Status string `json:"status"` // added | modified | removed
	Diff   string `json:"diff"`   // unified, three lines of context
}

// ChangeDiff is what changed in Git for a change's contract since it was
// last applied: the contract and every object it refers to, directly or
// through others, and the Policy and Platform that shape every plan.
type ChangeDiff struct {
	From    string       `json:"from,omitempty"` // the applied snapshot's label; empty if never applied
	To      string       `json:"to"`
	Objects []ObjectDiff `json:"objects"`
}

// changeDiff answers GET /api/v1/changes/{id}/diff.
func (s *Server) changeDiff(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := s.store.Q()
	c, err := q.Change(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.full(r, []string{c.Contract}) {
		forbidden(w)
		return
	}
	if s.cfg.Repos == nil || len(c.Snapshot) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server does not follow a repository"})
		return
	}
	var to repo.Snapshot
	if err := json.Unmarshal(c.Snapshot, &to); err != nil {
		s.fail(w, err)
		return
	}
	newG, err := s.cfg.Repos.Graph(ctx, to)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := ChangeDiff{To: to.Label(), Objects: []ObjectDiff{}}
	var oldG *catalog.Graph
	switch st, err := q.ContractState(ctx, c.Contract); {
	case err == nil && len(st.AppliedSnapshot) > 0:
		var from repo.Snapshot
		if err := json.Unmarshal(st.AppliedSnapshot, &from); err != nil {
			s.fail(w, err)
			return
		}
		if oldG, err = s.cfg.Repos.Graph(ctx, from); err != nil {
			s.fail(w, err)
			return
		}
		out.From = from.Label()
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		s.fail(w, err)
		return
	}
	keys := map[catalog.Key]bool{}
	for _, g := range []*catalog.Graph{oldG, newG} {
		if g == nil {
			continue
		}
		for _, k := range []string{catalog.KindServer, catalog.KindOperation, catalog.KindSchedule} {
			reach(g, catalog.Key{Kind: k, Name: c.Contract}, keys)
		}
		for _, k := range []string{catalog.KindPolicy, catalog.KindPlatform} {
			for _, o := range g.Catalog.List(k) {
				keys[o.Key()] = true
			}
		}
	}
	for k := range keys {
		d, err := objectDiff(oldG, newG, k)
		if err != nil {
			s.fail(w, err)
			return
		}
		if d != nil {
			out.Objects = append(out.Objects, *d)
		}
	}
	// The contract first, then by kind and name.
	sort.Slice(out.Objects, func(i, j int) bool {
		a, b := out.Objects[i], out.Objects[j]
		if (a.Name == c.Contract) != (b.Name == c.Contract) {
			return a.Name == c.Contract
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	writeJSON(w, http.StatusOK, out)
}

// reach adds k and everything it refers to, transitively.
func reach(g *catalog.Graph, k catalog.Key, seen map[catalog.Key]bool) {
	if _, ok := g.Catalog.Get(k.Kind, k.Name); !ok || seen[k] {
		return
	}
	seen[k] = true
	for _, r := range g.Refs(k) {
		reach(g, r.To, seen)
	}
}

func objectYAML(g *catalog.Graph, k catalog.Key) (text, file string, err error) {
	if g == nil {
		return "", "", nil
	}
	o, ok := g.Catalog.Get(k.Kind, k.Name)
	if !ok {
		return "", "", nil
	}
	b, err := o.YAML()
	return string(b), o.Source.File, err
}

func objectDiff(oldG, newG *catalog.Graph, k catalog.Key) (*ObjectDiff, error) {
	a, fileA, err := objectYAML(oldG, k)
	if err != nil {
		return nil, err
	}
	b, fileB, err := objectYAML(newG, k)
	if err != nil {
		return nil, err
	}
	if a == b {
		return nil, nil
	}
	d := &ObjectDiff{Kind: k.Kind, Name: k.Name, File: fileB, Status: "modified"}
	switch {
	case a == "":
		d.Status = "added"
	case b == "":
		d.Status, d.File = "removed", fileA
	}
	d.Diff, err = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(a), B: difflib.SplitLines(b), FromFile: fileA, ToFile: fileB, Context: 3,
	})
	return d, err
}
