package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// revertCommit has Gaia write the revert of a commit (design 7.6). The
// revert is a commit like any: its changes go through the same gates, and
// the person who asked for it may not approve them. Operators may revert
// any commit; others only one whose changes were all to their contracts.
func (s *Server) revertCommit(w http.ResponseWriter, r *http.Request) {
	p := auth.From(r.Context())
	if s.cfg.Writer == nil || s.cfg.Catalog == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "Gaia writes no commits here: gaia server has no --signing-key"})
		return
	}
	sha := r.PathValue("sha")
	if !fullSHA.MatchString(sha) {
		badRequest(w, errors.New("name the commit by its full SHA-1"))
		return
	}
	var b struct {
		Repo string `json:"repo"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&b); err != nil || b.Repo == "" {
		badRequest(w, errors.New("say which repository the commit is in (repo)"))
		return
	}
	by := "api"
	if p != nil {
		by = p.Username
		if !p.Has("operator") {
			changes, err := s.store.Q().Changes(r.Context(), db.ChangesParams{CommitSha: sha, MaxRows: 500})
			if err != nil {
				s.fail(w, err)
				return
			}
			var contracts []string
			for _, c := range changes {
				contracts = append(contracts, c.Contract)
			}
			if len(contracts) == 0 || !s.full(r, contracts) {
				writeJSON(w, http.StatusForbidden, Problem{Error: "only an operator may revert a commit that changed contracts you do not own"})
				return
			}
		}
	}
	snap, _ := s.cfg.Catalog.Snapshot()
	revs := append([]repo.Rev{snap.Platform}, snap.Contracts...)
	if snap.Config != nil {
		revs = append(revs, *snap.Config)
	}
	var target *repo.Rev
	for i := range revs {
		if revs[i].Name == b.Repo {
			target = &revs[i]
		}
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, Problem{Error: "Gaia does not follow a repository named " + b.Repo})
		return
	}
	h, err := s.cfg.Writer.Revert(r.Context(), target.Repo(), sha, by)
	switch {
	case errors.Is(err, repo.ErrConflict), errors.Is(err, repo.ErrMerge), errors.Is(err, repo.ErrMoved), errors.Is(err, repo.ErrNotOnRef):
		writeJSON(w, http.StatusConflict, Problem{Error: err.Error()})
		return
	case errors.Is(err, repo.ErrNoCommit):
		writeJSON(w, http.StatusNotFound, Problem{Error: err.Error()})
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.cfg.Log.Info("revert", "repo", b.Repo, "commit", sha, "revert", h, "by", by)
	s.cfg.Catalog.Kick()
	writeJSON(w, http.StatusCreated, map[string]string{"commit": h, "reverts": sha})
}
