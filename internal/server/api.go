package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// route is one API endpoint; api/openapi.yaml describes each of them.
// role is the Gaia role it needs; "" is anyone signed in, and what they
// see of each contract follows the visibility rule (design 11.4).
type route struct {
	method, path, role string
	h                  http.HandlerFunc
}

func (s *Server) apiRoutes() []route {
	return []route{
		{"GET", "/api/v1/runs", "", s.listRuns},
		{"GET", "/api/v1/runs/{id}", "", s.getRun},
		{"POST", "/api/v1/runs/{id}/cancel", "operator", s.cancelRun},
		{"GET", "/api/v1/runs/{id}/events", "", s.listEvents},
		{"GET", "/api/v1/runs/{id}/events/stream", "", s.streamEvents},
		{"GET", "/api/v1/runs/{id}/record", "", s.getRecord},
		{"GET", "/api/v1/records/verify", "", s.verifyRecords},
		{"GET", "/api/v1/contracts", "", s.listContracts},
		{"GET", "/api/v1/contracts/{name}", "", s.getContract},
		{"GET", "/api/v1/changes", "", s.listChanges},
		{"GET", "/api/v1/drift", "", s.listDrift},
		{"GET", "/api/v1/discovery", "operator", s.getDiscovery},
		{"POST", "/api/v1/discovery", "operator", s.requestDiscovery},
		{"POST", "/api/v1/contracts/{name}/reconcile", "operator", s.reconcileContract},
		{"GET", "/api/v1/changes/{id}", "", s.getChange},
		{"POST", "/api/v1/changes/{id}/retry", "operator", s.retryChange},
		{"GET", "/api/v1/changes/{id}/notifications", "", s.changeNotifications},
		{"GET", "/api/v1/changes/{id}/diff", "", s.changeDiff},
		{"POST", "/api/v1/webhooks/github", public, s.githubWebhook},
		{"POST", "/api/v1/commits/{sha}/revert", "", s.revertCommit},
		{"GET", "/api/v1/preview", "", s.preview},
		{"GET", "/api/v1/sources", "", s.listSources},
		{"GET", "/api/v1/catalog", "", s.listCatalog},
		{"GET", "/api/v1/catalog/{kind}/{name}", "", s.getCatalogObject},
		{"GET", "/api/v1/providers", "platform-admin", s.listProviders},
		{"POST", "/api/v1/providers/check", "platform-admin", s.checkProviders},
		{"GET", "/api/v1/operations", "operator", s.listOperations},
		{"GET", "/api/v1/schedules", "operator", s.listSchedules},
		{"GET", "/api/v1/access", "", s.listAccess},
		{"POST", "/api/v1/sources/sync", "platform-admin", s.syncSources},
		{"POST", "/api/v1/changes/approve", "", s.strong(s.approveChanges)},
		{"POST", "/api/v1/changes/{id}/approve", "", s.strong(s.decideChange(true))},
		{"POST", "/api/v1/changes/{id}/reject", "", s.strong(s.decideChange(false))},
	}
}

// public marks a route that needs no sign-in; it checks the caller itself.
const public = "public"

// full reports whether the caller sees every contract of the list in full.
func (s *Server) full(r *http.Request, contracts []string) bool {
	if s.cfg.Auth == nil {
		return true
	}
	p := auth.From(r.Context())
	for _, c := range contracts {
		if s.cfg.Auth.Access(p, c) != auth.Full {
			return false
		}
	}
	return true
}

// forbidden answers for a contract whose details the caller may not see.
func forbidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, Problem{Error: "you may see this contract's name only"})
}

// Run is a run as the API shows it.
type Run struct {
	ID          string     `json:"id"`
	Contracts   []string   `json:"contracts"`
	Commit      string     `json:"commit"`
	PlanHash    string     `json:"planHash"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	RequestedBy string     `json:"requestedBy,omitempty"`
	QueuedAt    time.Time  `json:"queuedAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	// ConfigCommit is the Ansible repository's commit the run used, and
	// EEImage its execution environment.
	ConfigCommit string  `json:"configCommit,omitempty"`
	EEImage      string  `json:"eeImage,omitempty"`
	Stages       []Stage `json:"stages,omitempty"`
	Record       *Record `json:"record,omitempty"`
	// Change is the change the run applies, and Hosts the hostnames of the
	// servers its events name by UID (detail only).
	Change string            `json:"change,omitempty"`
	Hosts  map[string]string `json:"hosts,omitempty"`
	// Restricted: the caller sees this run's contracts by name only, so its
	// details are left out.
	Restricted bool `json:"restricted,omitempty"`
}

// Stage is one stage of a run for one contract ("" for the whole run).
type Stage struct {
	Contract   string     `json:"contract"`
	Stage      string     `json:"stage"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Record is a run record's place in the chain.
type Record struct {
	Seq        int64  `json:"seq"`
	RecordHash string `json:"recordHash"`
	PrevHash   string `json:"prevHash"`
	Archived   string `json:"archived,omitempty"`
}

// Event is one event of a run.
type Event struct {
	Seq       int32           `json:"seq"`
	Stage     string          `json:"stage"`
	Type      string          `json:"type"`
	Host      string          `json:"host,omitempty"`
	Task      string          `json:"task,omitempty"`
	Changed   bool            `json:"changed,omitempty"`
	Failed    bool            `json:"failed,omitempty"`
	Stdout    string          `json:"stdout,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// ContractSummary is a contract in the list.
type ContractSummary struct {
	Name       string `json:"name"`
	LiveHosts  int32  `json:"liveHosts"`
	LastRun    string `json:"lastRun,omitempty"`
	LastStatus string `json:"lastStatus,omitempty"`
	// Sync is synced, out-of-sync or unknown (design 7.6).
	Sync       string `json:"sync,omitempty"`
	OpenChange string `json:"openChange,omitempty"`
	// Labels are the Server object's labels in Git (owner, env, app…).
	Labels map[string]string `json:"labels,omitempty"`
	// AppliedLabel is the commits it was last applied at, and AppliedAt
	// when.
	AppliedLabel string     `json:"appliedLabel,omitempty"`
	AppliedAt    *time.Time `json:"appliedAt,omitempty"`
	Restricted   bool       `json:"restricted,omitempty"`
}

// Contract is one contract with its servers and recent runs.
type Contract struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	// State is how Gaia last found it against Git (design 7.6).
	State      *ContractState `json:"state,omitempty"`
	OpenChange string         `json:"openChange,omitempty"`
	Hosts      []Host         `json:"hosts"`
	Runs       []Run          `json:"runs"`
}

// ContractState is a contract's sync and what was last applied: the
// commits, the plan's hash and the plan itself.
type ContractState struct {
	Sync            string          `json:"sync"`
	Error           string          `json:"error,omitempty"`
	EvaluatedAt     *time.Time      `json:"evaluatedAt,omitempty"`
	AppliedLabel    string          `json:"appliedLabel,omitempty"`
	AppliedPlanHash string          `json:"appliedPlanHash,omitempty"`
	AppliedAt       *time.Time      `json:"appliedAt,omitempty"`
	AppliedPlan     json.RawMessage `json:"appliedPlan,omitempty"`
}

// Host is a live server of a contract and its IDs in other systems.
type Host struct {
	UID         string            `json:"uid"`
	Hostname    string            `json:"hostname"`
	Site        string            `json:"site"`
	Network     string            `json:"network"`
	Status      string            `json:"status"`
	ExternalIDs map[string]string `json:"externalIds"` // provider/kind -> ID
}

// Problem is the body of every error response.
type Problem struct {
	Error string `json:"error"`
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 50, 1, 500)
	if err != nil {
		badRequest(w, err)
		return
	}
	rows, err := s.store.Q().Runs(r.Context(), db.RunsParams{Contract: r.URL.Query().Get("contract"), MaxRows: int32(limit)}) //nolint:gosec // bounded above
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]Run, 0, len(rows))
	for _, row := range rows {
		run := runJSON(row)
		if !s.full(r, row.Contracts) {
			run = Run{ID: run.ID, Contracts: run.Contracts, Status: run.Status, QueuedAt: run.QueuedAt, Restricted: true}
		}
		out = append(out, run)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := s.store.Q()
	row, err := q.Run(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.full(r, row.Contracts) {
		forbidden(w)
		return
	}
	run := runJSON(row)
	stages, err := q.RunStages(ctx, row.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, st := range stages {
		run.Stages = append(run.Stages, Stage{
			Contract: st.Contract, Stage: st.Stage, Status: st.Status, Detail: st.Detail.String,
			StartedAt: st.StartedAt.Time, FinishedAt: timePtr(st.FinishedAt),
		})
	}
	run.Hosts = map[string]string{}
	for _, c := range row.Contracts {
		hosts, err := q.LiveHosts(ctx, c)
		if err != nil {
			s.fail(w, err)
			return
		}
		for _, h := range hosts {
			run.Hosts[h.Uid] = h.Hostname
		}
	}
	rec, err := q.RunRecord(ctx, row.ID)
	switch {
	case err == nil:
		run.Record = &Record{Seq: rec.Seq, RecordHash: rec.RecordHash, PrevHash: rec.PrevHash, Archived: rec.Archived.String}
		// Servers gone since the run are still named in its record.
		var body struct {
			Changes []struct{ UID, Hostname string } `json:"changes"`
		}
		if json.Unmarshal([]byte(rec.Body), &body) == nil {
			for _, c := range body.Changes {
				if _, ok := run.Hosts[c.UID]; !ok && c.UID != "" && c.Hostname != "" {
					run.Hosts[c.UID] = c.Hostname
				}
			}
		}
	case !errors.Is(err, pgx.ErrNoRows):
		s.fail(w, err)
		return
	}
	switch id, err := q.ChangeOfRun(ctx, pgtype.Text{String: row.ID, Valid: true}); {
	case err == nil:
		run.Change = id
	case !errors.Is(err, pgx.ErrNoRows):
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	by := "api"
	if p := auth.From(r.Context()); p != nil {
		by = p.Username
	}
	err := queue.Cancel(r.Context(), s.store, s.queue, r.PathValue("id"), by)
	switch {
	case errors.Is(err, queue.ErrFinished):
		writeJSON(w, http.StatusConflict, Problem{Error: err.Error()})
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
	}
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	after, err := intParam(r, "after", 0, 0, 1<<31-1)
	if err != nil {
		badRequest(w, err)
		return
	}
	limit, err := intParam(r, "limit", 1000, 1, 5000)
	if err != nil {
		badRequest(w, err)
		return
	}
	id := r.PathValue("id")
	if !s.visibleRun(w, r, id) {
		return
	}
	evs, err := s.events(r.Context(), id, after, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

func (s *Server) events(ctx context.Context, run string, after, limit int) ([]Event, error) {
	rows, err := s.store.Q().RunEventsAfter(ctx, db.RunEventsAfterParams{RunID: run, Seq: int32(after), Limit: int32(limit)}) //nolint:gosec // bounded by the callers
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(rows))
	for _, e := range rows {
		ev := Event{
			Seq: e.Seq, Stage: e.Stage, Type: e.Event, Host: e.HostUid.String, Task: e.Task.String,
			Changed: e.Changed, Failed: e.Failed, Stdout: e.Stdout.String, CreatedAt: e.CreatedAt.Time,
		}
		if len(e.Data) > 0 {
			ev.Data = json.RawMessage(e.Data)
		}
		out = append(out, ev)
	}
	return out, nil
}

// keepAlive is how often an idle stream sends a comment, so that proxies
// keep it open.
const keepAlive = 15 * time.Second

// streamEvents sends a run's events as server-sent events: the ones after
// Last-Event-ID (or ?after) first, then each new one as it is written.
// The stream ends with an "end" event once the run has finished.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.visibleRun(w, r, id) {
		return
	}
	last, err := intParam(r, "after", 0, 0, 1<<31-1)
	if err != nil {
		badRequest(w, err)
		return
	}
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if last, err = strconv.Atoi(v); err != nil {
			badRequest(w, fmt.Errorf("Last-Event-ID: %w", err))
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, errors.New("streaming not supported"))
		return
	}
	conn, err := s.store.Pool().Acquire(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "listen "+apply.EventsChannel); err != nil {
		s.fail(w, err)
		return
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "unlisten *") }()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		// Events first, then the status: an event written just before the
		// run finished is never missed.
		for {
			evs, err := s.events(ctx, id, last, 500)
			if err != nil {
				return
			}
			for _, e := range evs {
				data, _ := json.Marshal(e)
				if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, eventName(e.Type), data); err != nil {
					return
				}
				last = int(e.Seq)
			}
			flusher.Flush()
			if len(evs) < 500 {
				break
			}
		}
		run, err := s.store.Q().Run(ctx, id)
		if err != nil {
			return
		}
		if run.Status != "queued" && run.Status != "running" {
			evs, err := s.events(ctx, id, last, 1)
			if err == nil && len(evs) == 0 {
				data, _ := json.Marshal(map[string]string{"status": run.Status})
				_, _ = fmt.Fprintf(w, "event: end\ndata: %s\n\n", data)
				flusher.Flush()
				return
			}
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, keepAlive)
		_, err = conn.Conn().WaitForNotification(wctx)
		cancel()
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			// No notification: say we are alive and look again.
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// eventName is the SSE event name: "log" for output lines, "ansible" for
// the rest.
func eventName(t string) string {
	if t == "log" {
		return "log"
	}
	return "ansible"
}

// visibleRun answers 404 or 403 itself when the run does not exist or the
// caller may not see it.
func (s *Server) visibleRun(w http.ResponseWriter, r *http.Request, id string) bool {
	run, err := s.store.Q().Run(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return false
	}
	if !s.full(r, run.Contracts) {
		forbidden(w)
		return false
	}
	return true
}

func (s *Server) getRecord(w http.ResponseWriter, r *http.Request) {
	if !s.visibleRun(w, r, r.PathValue("id")) {
		return
	}
	rec, err := s.store.Q().RunRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"seq": rec.Seq, "recordHash": rec.RecordHash, "prevHash": rec.PrevHash, "archived": rec.Archived.String,
		"body": json.RawMessage(rec.Body),
	})
}

func (s *Server) verifyRecords(w http.ResponseWriter, r *http.Request) {
	n, err := runrecord.Verify(r.Context(), s.store.Q())
	var broken *runrecord.Broken
	switch {
	case errors.As(err, &broken):
		writeJSON(w, http.StatusOK, map[string]any{"records": n, "intact": false, "broken": map[string]any{"seq": broken.Seq, "run": broken.Run, "reason": broken.Reason}})
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"records": n, "intact": true})
	}
}

func (s *Server) listContracts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Q().ContractSummaries(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]ContractSummary, 0, len(rows))
	for _, c := range rows {
		if !s.full(r, []string{c.Name}) {
			out = append(out, ContractSummary{Name: c.Name, Restricted: true})
			continue
		}
		cs := ContractSummary{Name: c.Name, LiveHosts: c.LiveHosts, LastRun: c.LastRun, LastStatus: c.LastStatus, Labels: s.labels(c.Name)}
		if st, err := s.store.Q().ContractState(r.Context(), c.Name); err == nil {
			cs.Sync, cs.AppliedLabel, cs.AppliedAt = st.Sync, st.AppliedLabel.String, timePtr(st.AppliedAt)
		}
		if oc, err := s.store.Q().OpenChange(r.Context(), c.Name); err == nil {
			cs.OpenChange = oc.ID
		}
		out = append(out, cs)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getContract(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := s.store.Q()
	name := r.PathValue("name")
	hosts, err := q.LiveHosts(ctx, name)
	if err == nil && !s.full(r, []string{name}) {
		forbidden(w)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	runs, err := q.Runs(ctx, db.RunsParams{Contract: name, MaxRows: 20})
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(hosts) == 0 && len(runs) == 0 {
		writeJSON(w, http.StatusNotFound, Problem{Error: "no such contract: " + name})
		return
	}
	c := Contract{Name: name, Labels: s.labels(name), Hosts: []Host{}, Runs: []Run{}}
	switch st, err := q.ContractState(ctx, name); {
	case err == nil:
		c.State = &ContractState{
			Sync: st.Sync, Error: st.Error.String, EvaluatedAt: timePtr(st.EvaluatedAt),
			AppliedLabel: st.AppliedLabel.String, AppliedPlanHash: st.AppliedPlanHash.String, AppliedAt: timePtr(st.AppliedAt),
		}
		if len(st.AppliedPlan) > 0 {
			c.State.AppliedPlan = json.RawMessage(st.AppliedPlan)
		}
	case !errors.Is(err, pgx.ErrNoRows):
		s.fail(w, err)
		return
	}
	switch oc, err := q.OpenChange(ctx, name); {
	case err == nil:
		c.OpenChange = oc.ID
	case !errors.Is(err, pgx.ErrNoRows):
		s.fail(w, err)
		return
	}
	for _, h := range hosts {
		ids, err := q.ExternalIDs(ctx, h.Uid)
		if err != nil {
			s.fail(w, err)
			return
		}
		host := Host{UID: h.Uid, Hostname: h.Hostname, Site: h.Site, Network: h.Network, Status: string(h.Status), ExternalIDs: map[string]string{}}
		for _, e := range ids {
			host.ExternalIDs[e.Provider+"/"+e.Kind] = e.ExternalID
		}
		c.Hosts = append(c.Hosts, host)
	}
	for _, row := range runs {
		c.Runs = append(c.Runs, runJSON(row))
	}
	writeJSON(w, http.StatusOK, c)
}

// labels are a contract's labels in the desired state, when the server
// follows a repository and the contract is a Server object there.
func (s *Server) labels(name string) map[string]string {
	if s.cfg.Catalog == nil {
		return nil
	}
	g := s.cfg.Catalog.Graph()
	if g == nil {
		return nil
	}
	if o, ok := g.Catalog.Get(catalog.KindServer, name); ok {
		return o.Metadata.Labels
	}
	return nil
}

func runJSON(r db.Run) Run {
	return Run{
		ID: r.ID, Contracts: r.Contracts, Commit: r.Commit, PlanHash: r.PlanHash, Status: r.Status,
		Error: r.Error.String, RequestedBy: r.RequestedBy.String, QueuedAt: r.QueuedAt.Time,
		StartedAt: timePtr(r.StartedAt), FinishedAt: timePtr(r.FinishedAt),
		ConfigCommit: r.ConfigCommit.String, EEImage: r.EeImage.String,
	}
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

func intParam(r *http.Request, name string, def, lo, hi int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s: want a number from %d to %d", name, lo, hi)
	}
	return n, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, Problem{Error: err.Error()})
}

// fail answers with 404 for a missing row and 500 for anything else; the
// details of a 500 go to the log, not to the client.
func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, Problem{Error: "not found"})
		return
	}
	s.cfg.Log.Error("api", "err", err)
	writeJSON(w, http.StatusInternalServerError, Problem{Error: "internal error"})
}
