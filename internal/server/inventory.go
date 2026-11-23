package server

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Warewave-Technology/gaia/internal/hostconn"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// The Activity and Inventory views (T27; docs/design/v2/Activity.dc.html, Inventory.dc.html): one request per table,
// in pages, so a list of thousands scrolls without loading every row.

// ActivityView is the Activity screen: what is running and queued now, what finished in the last hour, and the jobs
// that match the filter, newest first.
type ActivityView struct {
	Clock    ServerClock         `json:"clock"`
	Open     []store.JobCount    `json:"open"`      // queued and running jobs by kind
	LastHour []store.JobCount    `json:"last_hour"` // jobs that finished in the last hour, by kind and state
	Total    int                 `json:"total"`     // jobs that match the filter
	Rows     []store.ActivityRow `json:"rows"`
	Next     string              `json:"next"` // pass as before= for the next page; empty on the last
}

func (s *Server) activityView(c *call) (int, any, error) {
	ctx := c.r.Context()
	now := s.now()
	q := c.r.URL.Query()
	f := store.ActivityFilter{Kind: q.Get("kind"), State: q.Get("state")}
	var err error
	if f.HostID, err = c.intQuery("host_id", 0, 0); err != nil {
		return 0, nil, err
	}
	if f.Before, err = c.intQuery("before", 0, 0); err != nil {
		return 0, nil, err
	}
	n, err := c.intQuery("limit", 50, 200)
	if err != nil {
		return 0, nil, err
	}
	if n == 0 {
		n = 50
	}
	f.Limit = int(n)
	out := ActivityView{Clock: serverClock(now)}
	if out.Rows, out.Total, err = s.db.Activity(ctx, f); err != nil {
		return 0, nil, err
	}
	if out.Open, err = s.db.OpenJobCounts(ctx); err != nil {
		return 0, nil, err
	}
	if out.LastHour, err = s.db.FinishedSince(ctx, now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		return 0, nil, err
	}
	if len(out.Rows) == f.Limit {
		out.Next = strconv.FormatInt(out.Rows[len(out.Rows)-1].ID, 10)
	}
	return http.StatusOK, out, nil
}

// HostsView is the Inventory's host table.
type HostsView struct {
	Clock ServerClock     `json:"clock"`
	Total int             `json:"total"`
	Rows  []store.HostRow `json:"rows"`
	Next  string          `json:"next"` // pass as after= for the next page; empty on the last
}

func (s *Server) hostsView(c *call) (int, any, error) {
	off, n, err := offsetPage(c, 100)
	if err != nil {
		return 0, nil, err
	}
	out := HostsView{Clock: serverClock(s.now())}
	if out.Rows, out.Total, err = s.db.HostRows(c.r.Context(), off, n); err != nil {
		return 0, nil, err
	}
	for i, r := range out.Rows {
		if r.PresentedFingerprint == "" {
			continue
		}
		// The admin compares both before accepting (ADR-007 C17); the pinned key was read to see it differ.
		if out.Rows[i].PinnedFingerprint, err = hostconn.KeyFingerprint(r.SSHHostKey); err != nil {
			return 0, nil, fmt.Errorf("host %s: pinned key: %w", r.Name, err)
		}
	}
	if off+n < out.Total {
		out.Next = strconv.Itoa(off + n)
	}
	return http.StatusOK, out, nil
}

// RepositoriesView is the Inventory's repository table.
type RepositoriesView struct {
	Clock ServerClock           `json:"clock"`
	Total int                   `json:"total"`
	Rows  []store.RepositoryRow `json:"rows"`
	Next  string                `json:"next"` // pass as after= for the next page; empty on the last
}

func (s *Server) repositoriesView(c *call) (int, any, error) {
	off, n, err := offsetPage(c, 100)
	if err != nil {
		return 0, nil, err
	}
	out := RepositoriesView{Clock: serverClock(s.now())}
	if out.Rows, out.Total, err = s.db.RepositoryRows(c.r.Context(), off, n); err != nil {
		return 0, nil, err
	}
	if off+n < out.Total {
		out.Next = strconv.Itoa(off + n)
	}
	return http.StatusOK, out, nil
}

// AuditView is the Inventory's audit log: newest first, in pages.
type AuditView struct {
	Clock ServerClock        `json:"clock"`
	Total int                `json:"total"`
	Rows  []store.AuditEntry `json:"rows"`
	Next  string             `json:"next"` // pass as before= for the next page; empty on the last
}

func (s *Server) auditView(c *call) (int, any, error) {
	before, err := c.intQuery("before", 0, 0)
	if err != nil {
		return 0, nil, err
	}
	n, err := c.intQuery("limit", 100, 500)
	if err != nil {
		return 0, nil, err
	}
	if n == 0 {
		n = 100
	}
	out := AuditView{Clock: serverClock(s.now())}
	if out.Rows, out.Total, err = s.db.AuditNewest(c.r.Context(), before, int(n)); err != nil {
		return 0, nil, err
	}
	if len(out.Rows) == int(n) && out.Rows[len(out.Rows)-1].Seq > 1 {
		out.Next = strconv.FormatInt(out.Rows[len(out.Rows)-1].Seq, 10)
	}
	return http.StatusOK, out, nil
}
