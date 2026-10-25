package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/proposal"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Proposal is a settings change (design 17.3).
type Proposal struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	Reason         string            `json:"reason,omitempty"`
	Status         string            `json:"status"`
	BaseVersion    int64             `json:"baseVersion"`
	ContentHash    string            `json:"contentHash"`
	RequestedBy    string            `json:"requestedBy"`
	Critical       bool              `json:"critical"`
	WhyCritical    []string          `json:"whyCritical,omitempty"`
	Impact         []proposal.Impact `json:"impact"`
	Paths          []string          `json:"paths"`
	DecidedBy      string            `json:"decidedBy,omitempty"`
	Comment        string            `json:"comment,omitempty"`
	AppliedVersion int64             `json:"appliedVersion,omitempty"`
	StaleReason    string            `json:"staleReason,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
	DecidedAt      *time.Time        `json:"decidedAt,omitempty"`
	// MayDecide says whether the caller may approve or reject it, and
	// Why not when they may not.
	MayDecide bool   `json:"mayDecide"`
	Why       string `json:"why,omitempty"`
	// Objects is what it does to each object (on its own page).
	Objects []proposal.Object `json:"objects,omitempty"`
}

func (s *Server) proposalJSON(r *http.Request, pr db.SettingsProposal) Proposal {
	out := Proposal{
		ID: pr.ID, Title: pr.Title, Reason: pr.Reason, Status: pr.Status, BaseVersion: pr.BaseVersion, ContentHash: pr.ContentHash,
		RequestedBy: pr.RequestedBy, Critical: pr.Critical, WhyCritical: pr.WhyCritical, Impact: []proposal.Impact{},
		DecidedBy: pr.DecidedBy.String, Comment: pr.Comment.String, AppliedVersion: pr.AppliedVersion.Int64, StaleReason: pr.StaleReason.String,
		CreatedAt: pr.CreatedAt.Time, DecidedAt: timePtr(pr.DecidedAt),
	}
	_ = json.Unmarshal(pr.Impact, &out.Impact)
	for p := range proposal.Changes(pr) {
		out.Paths = append(out.Paths, p)
	}
	slices.Sort(out.Paths)
	if pr.Status == "pending" {
		if err := s.cfg.Proposals.MayDecide(r.Context(), person(r), pr); err != nil {
			out.Why = err.Error()
		} else {
			out.MayDecide = true
		}
	}
	return out
}

func person(r *http.Request) proposal.Person {
	p := proposal.Person{Username: "anonymous", Address: clientAddress(r)}
	if pr := auth.From(r.Context()); pr != nil {
		p.UserID, p.Username, p.SessionID, p.Teams, p.Roles = pr.UserID, pr.Username, pr.SessionID, pr.Teams, pr.Roles
	}
	return p
}

// proposer reports whether the caller may propose settings changes:
// operators and platform admins (everyone on a server without sign-in).
func (s *Server) proposer(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.Proposals == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server keeps no settings"})
		return false
	}
	if s.cfg.Auth == nil {
		return true
	}
	p := auth.From(r.Context())
	if p == nil || (!p.Has("operator") && !p.Has("platform-admin")) {
		writeJSON(w, http.StatusForbidden, Problem{Error: "settings changes are proposed by operators and platform admins"})
		return false
	}
	return true
}

// checkSettings says what a settings change would do, without keeping
// it: the modal's check before it is proposed.
func (s *Server) checkSettings(w http.ResponseWriter, r *http.Request) {
	if !s.proposer(w, r) {
		return
	}
	var b struct {
		Changes []proposal.Edit `json:"changes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b); err != nil {
		badRequest(w, err)
		return
	}
	c, err := s.cfg.Proposals.Check(r.Context(), b.Changes)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// propose records a settings change that checks out.
func (s *Server) propose(w http.ResponseWriter, r *http.Request) {
	if !s.proposer(w, r) {
		return
	}
	var b struct {
		Title   string          `json:"title"`
		Reason  string          `json:"reason"`
		Changes []proposal.Edit `json:"changes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b); err != nil {
		badRequest(w, err)
		return
	}
	pr, c, err := s.cfg.Proposals.Propose(r.Context(), person(r), b.Title, b.Reason, b.Changes)
	switch {
	case errors.Is(err, proposal.ErrInvalid):
		writeJSON(w, http.StatusUnprocessableEntity, struct {
			Problem
			Check *proposal.Check `json:"check"`
		}{Problem{Error: err.Error()}, c})
	case err != nil:
		s.fail(w, err)
	default:
		out := s.proposalJSON(r, *pr)
		out.Objects = c.Objects
		writeJSON(w, http.StatusCreated, out)
	}
}

// listProposals is the settings changes, newest first: ?status=pending,
// ?awaiting=me for the ones the caller may decide.
func (s *Server) listProposals(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Proposals == nil {
		writeJSON(w, http.StatusOK, []Proposal{})
		return
	}
	n, err := intParam(r, "limit", 100, 1, 500)
	if err != nil {
		badRequest(w, err)
		return
	}
	status := r.URL.Query().Get("status")
	awaiting := r.URL.Query().Get("awaiting") == "me"
	if awaiting {
		status = "pending"
	}
	rows, err := s.store.Q().SettingsProposals(r.Context(), db.SettingsProposalsParams{Status: pgtype.Text{String: status, Valid: status != ""}, MaxRows: int32(n)}) //nolint:gosec // bounded above
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Proposal, 0, len(rows))
	for _, pr := range rows {
		j := s.proposalJSON(r, pr)
		if awaiting && !j.MayDecide {
			continue
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, out)
}

// getProposal is one settings change with what it does to each object.
func (s *Server) getProposal(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Proposals == nil {
		writeJSON(w, http.StatusNotFound, Problem{Error: "no such settings change"})
		return
	}
	pr, err := s.store.Q().SettingsProposal(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	out := s.proposalJSON(r, pr)
	before := map[string]string{}
	base := pr.BaseVersion
	if pr.AppliedVersion.Valid && pr.AppliedVersion.Int64 > 1 {
		base = pr.AppliedVersion.Int64 - 1 // what it changed when it was applied
	}
	if files, err := s.cfg.Settings.Files(r.Context(), base); err == nil {
		for _, f := range files {
			before[f.Path] = string(f.Data)
		}
	}
	for _, p := range out.Paths {
		after := proposal.Changes(pr)[p]
		o := proposal.Object{Kind: catalog.SettingsFileKind(p), Name: strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".yaml"), Path: p, Before: before[p], After: string(after)}
		switch _, had := before[p]; {
		case after == nil:
			o.Op = "remove"
		case !had:
			o.Op = "add"
		default:
			o.Op = "change"
		}
		out.Objects = append(out.Objects, o)
	}
	writeJSON(w, http.StatusOK, out)
}

// decideProposal approves or rejects a settings change for the content
// the person saw.
func (s *Server) decideProposal(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Proposals == nil {
			writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server keeps no settings"})
			return
		}
		var b struct {
			ContentHash string `json:"contentHash"`
			Comment     string `json:"comment"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&b); err != nil || b.ContentHash == "" {
			badRequest(w, errors.New("send the contentHash you decide on"))
			return
		}
		pr, err := s.cfg.Proposals.Decide(r.Context(), person(r), proposal.Decision{ID: r.PathValue("id"), ContentHash: b.ContentHash, Approve: approve, Comment: b.Comment})
		s.proposalResult(w, r, pr, err)
	}
}

// withdrawProposal takes back one's own pending settings change.
func (s *Server) withdrawProposal(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Proposals == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server keeps no settings"})
		return
	}
	err := s.cfg.Proposals.Withdraw(r.Context(), person(r), r.PathValue("id"))
	var pr *db.SettingsProposal
	if err == nil {
		p, gerr := s.store.Q().SettingsProposal(r.Context(), r.PathValue("id"))
		pr, err = &p, gerr
	}
	s.proposalResult(w, r, pr, err)
}

func (s *Server) proposalResult(w http.ResponseWriter, r *http.Request, pr *db.SettingsProposal, err error) {
	switch {
	case errors.Is(err, proposal.ErrNotApprover), errors.Is(err, proposal.ErrOwn):
		writeJSON(w, http.StatusForbidden, Problem{Error: err.Error()})
	case errors.Is(err, proposal.ErrClosed), errors.Is(err, proposal.ErrContent), errors.Is(err, proposal.ErrStale):
		writeJSON(w, http.StatusConflict, Problem{Error: err.Error()})
	case err != nil && strings.HasPrefix(err.Error(), "say why"):
		badRequest(w, err)
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusOK, s.proposalJSON(r, *pr))
	}
}
