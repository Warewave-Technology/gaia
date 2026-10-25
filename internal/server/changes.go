package server

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/gaia/internal/auth"
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
	// AwaitingMe: the caller is one of those who may give an approval
	// the change still waits for.
	AwaitingMe bool `json:"awaitingMe,omitempty"`
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
	// ?awaiting=me: only the changes the caller may approve now.
	mine := r.URL.Query().Get("awaiting") == "me"
	p := auth.From(r.Context())
	if mine && p == nil {
		writeJSON(w, http.StatusOK, []Change{})
		return
	}
	rows, err := s.store.Q().Changes(r.Context(), db.ChangesParams{
		Contract: r.URL.Query().Get("contract"), OpenOnly: mine || r.URL.Query().Get("open") == "true", CommitSha: r.URL.Query().Get("commit"), MaxRows: int32(limit), //nolint:gosec // bounded above
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
		} else if ch.AwaitingMe, err = s.awaits(r, c); err != nil {
			s.fail(w, err)
			return
		}
		if mine && !ch.AwaitingMe {
			continue
		}
		out = append(out, ch)
	}
	writeJSON(w, http.StatusOK, out)
}

// awaits reports whether the change waits for an approval the caller may
// give: it is pending, its approval gate waits, the caller is a candidate
// of a group nobody approved yet, and the caller has not decided on it.
func (s *Server) awaits(r *http.Request, c db.Change) (bool, error) {
	p := auth.From(r.Context())
	if p == nil || c.Status != "pending" {
		return false, nil
	}
	ctx := r.Context()
	gates, err := s.store.Q().Gates(ctx, c.ID)
	if err != nil {
		return false, err
	}
	for _, g := range gates {
		if g.Gate != "approval" || g.Status != "waiting" {
			continue
		}
		var need change.Need
		if err := json.Unmarshal(g.Detail, &need); err != nil {
			return false, nil
		}
		for _, x := range need.Excluded {
			if x == p.Username {
				return false, nil
			}
		}
		open := false
		for _, gr := range need.Groups {
			if gr.ApprovedBy != "" {
				continue
			}
			for _, cand := range gr.Candidates {
				if cand == p.Username {
					open = true
				}
			}
		}
		if !open {
			return false, nil
		}
		decided, err := s.store.Q().Approvals(ctx, c.ID)
		if err != nil {
			return false, err
		}
		for _, a := range decided {
			if a.Username == p.Username && a.PlanHash == c.PlanHash {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

// Drift is a live drift report (design 7.5).
type Drift struct {
	Contract  string    `json:"contract"`
	UID       string    `json:"uid,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Area      string    `json:"area,omitempty"`
	Key       string    `json:"key,omitempty"`
	Want      string    `json:"want,omitempty"`
	Got       string    `json:"got,omitempty"`
	Status    string    `json:"status"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Change    string    `json:"change,omitempty"`
	// Restricted: the contract is not the caller's to see in full.
	Restricted bool `json:"restricted,omitempty"`
}

// listDrift is the live drift, of one contract with ?contract=.
func (s *Server) listDrift(w http.ResponseWriter, r *http.Request) {
	contract := pgtype.Text{String: r.URL.Query().Get("contract"), Valid: r.URL.Query().Get("contract") != ""}
	rows, err := s.store.Q().LiveDrift(r.Context(), contract)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Drift, 0, len(rows))
	for _, d := range rows {
		x := Drift{Contract: d.Contract, Status: d.Status, FirstSeen: d.FirstSeen.Time, LastSeen: d.LastSeen.Time}
		if s.full(r, []string{d.Contract}) {
			x.UID, x.Hostname, x.Area, x.Key, x.Want, x.Got, x.Change = d.Uid, d.Hostname, d.Area, d.Key, d.Want, d.Got, d.ChangeID.String
		} else {
			x.Restricted = true
		}
		out = append(out, x)
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
	if out.AwaitingMe, err = s.awaits(r, c); err != nil {
		s.fail(w, err)
		return
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

// getDiscovery is the latest discovery report (design 8.3).
func (s *Server) getDiscovery(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.Q().LatestDiscovery(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(d.Report)
}

// requestDiscovery has the next scan tick write a discovery report.
func (s *Server) requestDiscovery(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Q().ForgetDriftScan(r.Context(), "discovery"); err != nil {
		s.fail(w, err)
		return
	}
	if !s.audit(w, r, auditEntry(r, "discovery.request", "")) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}

// reconcileContract opens a change that applies a contract's applied
// plan again, putting back whatever drifted (design 7.5); its owner
// approves it.
func (s *Server) reconcileContract(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Changes == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server does not follow a repository"})
		return
	}
	name := r.PathValue("name")
	if !s.full(r, []string{name}) {
		forbidden(w)
		return
	}
	id, err := s.cfg.Changes.OpenDrift(r.Context(), name, false)
	switch {
	case err != nil:
		s.fail(w, err)
	case id == "":
		writeJSON(w, http.StatusConflict, Problem{Error: name + " has no applied plan to go back to, or is about to change"})
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"change": id})
	}
}

// strong asks for a recent passkey or MFA, when sign-in is on.
func (s *Server) strong(h http.HandlerFunc) http.HandlerFunc {
	if s.cfg.Auth == nil {
		return h
	}
	return s.cfg.Auth.Strong(h)
}

// decideChange approves or rejects a change for the plan the person saw
// (design 7.6). Only who the approval gate lists may decide.
func (s *Server) decideChange(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := auth.From(r.Context())
		if s.cfg.Changes == nil || p == nil {
			writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server does not take decisions"})
			return
		}
		var b struct {
			PlanHash string `json:"planHash"`
			Comment  string `json:"comment"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&b); err != nil || b.PlanHash == "" {
			badRequest(w, errors.New("send the planHash you decide on"))
			return
		}
		if !approve && strings.TrimSpace(b.Comment) == "" {
			badRequest(w, errors.New("say why you reject"))
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
		err = s.cfg.Changes.Decide(r.Context(), change.Decision{
			Change: c.ID, PlanHash: b.PlanHash, UserID: p.UserID, Username: p.Username, Approve: approve, Comment: strings.TrimSpace(b.Comment),
		})
		switch {
		case errors.Is(err, change.ErrNotApprover):
			writeJSON(w, http.StatusForbidden, Problem{Error: err.Error()})
		case errors.Is(err, change.ErrClosed), errors.Is(err, change.ErrStalePlan), errors.Is(err, change.ErrDecided), errors.Is(err, change.ErrNoApproval):
			writeJSON(w, http.StatusConflict, Problem{Error: err.Error()})
		case err != nil:
			s.fail(w, err)
		default:
			c, err := s.store.Q().Change(r.Context(), c.ID)
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, changeJSON(c))
		}
	}
}

// approveChanges approves several changes at once: those one commit
// brings to several owners, which a platform-admin may approve together
// (design 7.6). Each is decided on its own; the answer says how each went.
func (s *Server) approveChanges(w http.ResponseWriter, r *http.Request) {
	p := auth.From(r.Context())
	if s.cfg.Changes == nil || p == nil {
		writeJSON(w, http.StatusServiceUnavailable, Problem{Error: "this server does not take decisions"})
		return
	}
	var b struct {
		Changes []struct {
			ID       string `json:"id"`
			PlanHash string `json:"planHash"`
		} `json:"changes"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&b); err != nil || len(b.Changes) == 0 || len(b.Changes) > 100 {
		badRequest(w, errors.New("send 1 to 100 changes, each with the planHash you approve"))
		return
	}
	type result struct {
		ID     string `json:"id"`
		Status string `json:"status,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	out := make([]result, 0, len(b.Changes))
	for _, ch := range b.Changes {
		res := result{ID: ch.ID}
		c, err := s.store.Q().Change(r.Context(), ch.ID)
		switch {
		case err != nil:
			res.Error = "no such change"
		case !s.full(r, []string{c.Contract}):
			res.Error = "you may see this contract's name only"
		default:
			err = s.cfg.Changes.Decide(r.Context(), change.Decision{
				Change: c.ID, PlanHash: ch.PlanHash, UserID: p.UserID, Username: p.Username, Approve: true, Comment: strings.TrimSpace(b.Comment),
			})
			if err != nil {
				if !decisionError(err) {
					s.fail(w, err)
					return
				}
				res.Error = err.Error()
			} else if c, err = s.store.Q().Change(r.Context(), c.ID); err == nil {
				res.Status = c.Status
			}
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, out)
}

// decisionError reports whether err is a refused decision, not a failure.
func decisionError(err error) bool {
	for _, e := range []error{change.ErrNotApprover, change.ErrClosed, change.ErrStalePlan, change.ErrDecided, change.ErrNoApproval} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// Notification is one message Gaia sent, or is sending, about a change.
type Notification struct {
	ID        int64      `json:"id"`
	Trigger   string     `json:"trigger"`
	Channel   string     `json:"channel"`
	Recipient string     `json:"recipient"`
	Status    string     `json:"status"`
	Attempts  int32      `json:"attempts"`
	LastError string     `json:"lastError,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	SentAt    *time.Time `json:"sentAt,omitempty"`
}

func (s *Server) changeNotifications(w http.ResponseWriter, r *http.Request) {
	q := s.store.Q()
	c, err := q.Change(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.full(r, []string{c.Contract}) {
		forbidden(w)
		return
	}
	rows, err := q.Notifications(r.Context(), db.NotificationsParams{ChangeID: c.ID, MaxRows: 500})
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Notification, 0, len(rows))
	for _, n := range rows {
		out = append(out, Notification{ID: n.ID, Trigger: n.Trigger, Channel: n.Channel, Recipient: n.Recipient, Status: n.Status,
			Attempts: n.Attempts, LastError: n.LastError.String, CreatedAt: n.CreatedAt.Time, SentAt: timePtr(n.SentAt)})
	}
	writeJSON(w, http.StatusOK, out)
}
