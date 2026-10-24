package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/gaia/internal/change"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Change is a change as the API shows it (design 7.6).
type Change struct {
	ID          string          `json:"id"`
	Contract    string          `json:"contract"`
	Source      string          `json:"source"`
	Status      string          `json:"status"`
	Label       string          `json:"label,omitempty"`
	PlanHash    string          `json:"planHash,omitempty"`
	RequestedBy []string        `json:"requestedBy,omitempty"`
	Commits     json.RawMessage `json:"commits,omitempty"`
	Gates       []change.Gate   `json:"gates,omitempty"`
	Approvals   []Approval      `json:"approvals,omitempty"`
	Plan        json.RawMessage `json:"plan,omitempty"`
	Snapshot    json.RawMessage `json:"snapshot,omitempty"`
	RunID       string          `json:"runId,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	FinishedAt  *time.Time      `json:"finishedAt,omitempty"`
	Restricted  bool            `json:"restricted,omitempty"`
}

// Approval is one decision on a change.
type Approval struct {
	Username  string    `json:"username"`
	Decision  string    `json:"decision"`
	PlanHash  string    `json:"planHash"`
	Comment   string    `json:"comment,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func changeJSON(c db.Change) Change {
	return Change{
		ID: c.ID, Contract: c.Contract, Source: c.Source, Status: c.Status, Label: c.Label, PlanHash: c.PlanHash,
		RequestedBy: c.RequestedBy, Commits: json.RawMessage(c.Commits), RunID: c.RunID.String,
		CreatedAt: c.CreatedAt.Time, UpdatedAt: c.UpdatedAt.Time, FinishedAt: timePtr(c.FinishedAt),
	}
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 50, 1, 500)
	if err != nil {
		badRequest(w, err)
		return
	}
	rows, err := s.store.Q().Changes(r.Context(), db.ChangesParams{
		Contract: r.URL.Query().Get("contract"), OpenOnly: r.URL.Query().Get("open") == "true", MaxRows: int32(limit), //nolint:gosec // bounded above
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Change, 0, len(rows))
	for _, c := range rows {
		ch := changeJSON(c)
		if !s.full(r, []string{c.Contract}) {
			ch = Change{ID: c.ID, Contract: c.Contract, Status: c.Status, CreatedAt: ch.CreatedAt, UpdatedAt: ch.UpdatedAt, Restricted: true}
		}
		out = append(out, ch)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getChange(w http.ResponseWriter, r *http.Request) {
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
	out := changeJSON(c)
	out.Plan, out.Snapshot = json.RawMessage(c.Plan), json.RawMessage(c.Snapshot)
	gates, err := q.Gates(ctx, c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, g := range gates {
		out.Gates = append(out.Gates, change.Gate{Gate: g.Gate, Status: g.Status, Detail: json.RawMessage(g.Detail)})
	}
	approvals, err := q.Approvals(ctx, c.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, a := range approvals {
		out.Approvals = append(out.Approvals, Approval{Username: a.Username, Decision: a.Decision, PlanHash: a.PlanHash, Comment: a.Comment.String, CreatedAt: a.CreatedAt.Time})
	}
	writeJSON(w, http.StatusOK, out)
}

// retryChange opens a new change for a failed or cancelled one (operator).
func (s *Server) retryChange(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Changes == nil || s.cfg.Catalog == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server does not follow a repository"})
		return
	}
	c, err := s.store.Q().Change(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.full(r, []string{c.Contract}) {
		forbidden(w)
		return
	}
	snap, _ := s.cfg.Catalog.Snapshot()
	id, err := s.cfg.Changes.Retry(r.Context(), c.ID, snap, s.cfg.Catalog.Graph())
	switch {
	case errors.Is(err, change.ErrNotRetryable):
		writeJSON(w, http.StatusConflict, Problem{Error: err.Error()})
	case errors.Is(err, pgx.ErrNoRows):
		s.fail(w, err)
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"change": id})
	}
}
