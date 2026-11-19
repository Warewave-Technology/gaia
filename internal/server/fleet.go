package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// The fleet views (T24, ADR-007 C1; the v2 screens in docs/design/v2/): each answers one region of a screen in one
// request, from the status table, in pages. Nothing here recomputes a status.

// listWindow is how far back a VM list row's history strip reaches.
const listWindow = 24 * time.Hour

// FleetView is the overview's headline: can every VM return right now, and if not, how many cannot.
type FleetView struct {
	Clock      ServerClock      `json:"clock"`
	VMs        int              `json:"vms"`
	CanReturn  int              `json:"can_return"` // OK: a point a restore test proved within every target
	Cannot     int              `json:"cannot"`
	Causes     int              `json:"causes"` // needs-attention groups
	ByStatus   map[string]int   `json:"by_status"`
	Uncomputed int              `json:"uncomputed"`
	WorstRPO   *FleetWorst      `json:"worst_proven_rpo"`
	Running    []store.JobCount `json:"running"` // queued and running jobs by kind
	Ledger     LedgerSummary    `json:"ledger"`
	Kit        KitSummary       `json:"recovery_kit"`
	Notify     NotifySummary    `json:"notifications"`
	Recompute  *RecomputeStats  `json:"status_recompute"` // the newest full recompute of the status table
	CodesVer   int              `json:"reason_codes_version"`
}

// FleetWorst is the VM with the fleet's worst proven RPO.
type FleetWorst struct {
	VMID       int64   `json:"vm_id"`
	VM         string  `json:"vm"`
	ProvenRPOS float64 `json:"proven_rpo_s"`
	RPOTargetS *int64  `json:"rpo_target_s"`
}

func (s *Server) fleetView(c *call) (int, any, error) {
	ctx := c.r.Context()
	now := s.now()
	fc, err := s.db.FleetCounts(ctx)
	if err != nil {
		return 0, nil, err
	}
	gs, err := s.db.AttentionGroups(ctx)
	if err != nil {
		return 0, nil, err
	}
	out := FleetView{Clock: serverClock(now), VMs: fc.VMs, CanReturn: fc.OK, Cannot: fc.VMs - fc.OK, Causes: len(gs), Uncomputed: fc.Uncomputed,
		ByStatus: map[string]int{status.Critical: fc.Critical, status.Unknown: fc.Unknown, status.Warning: fc.Warning, status.OK: fc.OK},
		CodesVer: status.CodesVersion}
	if w := fc.WorstProven; w != nil {
		out.WorstRPO = &FleetWorst{VMID: w.VMID, VM: w.VM, ProvenRPOS: now.Sub(timeOf(w.ProvenTakenAt)).Seconds(), RPOTargetS: w.RPOTargetS}
	}
	if out.Running, err = s.db.OpenJobCounts(ctx); err != nil {
		return 0, nil, err
	}
	if out.Ledger, err = s.ledgerSummary(ctx); err != nil {
		return 0, nil, err
	}
	if out.Kit, err = kitSummary(ctx, s.cfg, s.db); err != nil {
		return 0, nil, err
	}
	if out.Notify, err = s.notifySummary(ctx); err != nil {
		return 0, nil, err
	}
	s.recomputed.mu.Lock()
	out.Recompute = s.recomputed.last
	s.recomputed.mu.Unlock()
	return http.StatusOK, out, nil
}

// AttentionView is "Needs attention": the VMs that are not OK, grouped by cause, in pages.
type AttentionView struct {
	Clock  ServerClock      `json:"clock"`
	Total  int              `json:"total"` // groups
	VMs    int              `json:"vms"`   // VMs in all groups
	Groups []AttentionGroup `json:"groups"`
	Next   string           `json:"next"` // pass as after= for the next page; empty on the last
}

// AttentionGroup is one cause: its code and scope, how many VMs, since when, where, and one VM's sentence.
type AttentionGroup struct {
	Status   string            `json:"status"`
	Code     string            `json:"code"`
	Cause    string            `json:"cause"` // the code's cause in words
	Scope    *status.Scope     `json:"scope"` // the shared cause; nil when the cause is each VM's own
	Count    int               `json:"count"`
	Since    string            `json:"since"`
	Example  AttentionExample  `json:"example"`
	Hosts    []store.NameCount `json:"hosts"`
	Policies []store.NameCount `json:"policies"`
}

// AttentionExample is the group's VM longest in this state, with its deciding reason as it reads now.
type AttentionExample struct {
	VMID   int64  `json:"vm_id"`
	VM     string `json:"vm"`
	Reason string `json:"reason"`
}

var rankWord = [...]string{status.Critical, status.Unknown, status.Warning, status.OK}

func (s *Server) attentionView(c *call) (int, any, error) {
	ctx := c.r.Context()
	now := s.now()
	off, n, err := offsetPage(c, 50)
	if err != nil {
		return 0, nil, err
	}
	gs, err := s.db.AttentionGroups(ctx)
	if err != nil {
		return 0, nil, err
	}
	out := AttentionView{Clock: serverClock(now), Total: len(gs), Groups: []AttentionGroup{}}
	for _, g := range gs {
		out.VMs += g.Count
	}
	for i := off; i < len(gs) && i < off+n; i++ {
		g := gs[i]
		ag := AttentionGroup{Status: rankWord[min(max(g.Rank, 0), 3)], Code: g.Code, Count: g.Count, Hosts: g.Hosts, Policies: g.Policies}
		if d, ok := status.Lookup(status.Code(g.Code)); ok {
			ag.Cause = d.Cause
		}
		if g.ScopeKind != "" {
			ag.Scope = &status.Scope{Kind: g.ScopeKind, ID: g.ScopeID, Name: g.ScopeName}
		}
		if g.Since != nil {
			ag.Since = timeOf(*g.Since).Format(time.RFC3339Nano)
		}
		if g.ExampleVM > 0 {
			r, _, err := s.storedStatus(ctx, g.ExampleVM)
			if err != nil {
				return 0, nil, err
			}
			vm, err := s.db.VM(ctx, g.ExampleVM)
			if err != nil {
				return 0, nil, err
			}
			ag.Example = AttentionExample{VMID: vm.ID, VM: vm.Name, Reason: r.Reasons[0].Text}
		}
		out.Groups = append(out.Groups, ag)
	}
	if off+n < len(gs) {
		out.Next = strconv.Itoa(off + n)
	}
	return http.StatusOK, out, nil
}

// CoverageView is "Coverage by host / policy / tag": every group's VMs by status, worst first; groups whose VMs are
// all OK are left out unless all=true, and counted.
type CoverageView struct {
	Clock   ServerClock     `json:"clock"`
	By      string          `json:"by"`
	Total   int             `json:"total"`   // groups
	Healthy int             `json:"healthy"` // groups with every VM OK
	Shown   int             `json:"shown"`   // groups in this answer, all pages
	Groups  []CoverageGroup `json:"groups"`
	Next    string          `json:"next"`
}

// CoverageGroup is one host, policy or tag.
type CoverageGroup struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	VMs          int            `json:"vms"`
	ByStatus     map[string]int `json:"by_status"`
	WorstRPO     *FleetWorst    `json:"worst_proven_rpo"`
	OldestProofS *float64       `json:"oldest_proof_s"` // the age of the group's oldest proof
}

func (s *Server) coverageView(c *call) (int, any, error) {
	ctx := c.r.Context()
	now := s.now()
	by := c.r.URL.Query().Get("by")
	if by == "" {
		by = "host"
	}
	all := c.r.URL.Query().Get("all") == "true"
	off, n, err := offsetPage(c, 50)
	if err != nil {
		return 0, nil, err
	}
	gs, err := s.db.Coverage(ctx, by)
	if err != nil {
		return 0, nil, err
	}
	out := CoverageView{Clock: serverClock(now), By: by, Total: len(gs), Groups: []CoverageGroup{}}
	var shown []store.CoverageGroup
	for _, g := range gs {
		if g.OK == g.VMs {
			out.Healthy++
			if !all {
				continue
			}
		}
		shown = append(shown, g)
	}
	out.Shown = len(shown)
	for i := off; i < len(shown) && i < off+n; i++ {
		g := shown[i]
		cg := CoverageGroup{ID: g.ID, Name: g.Name, VMs: g.VMs,
			ByStatus: map[string]int{status.Critical: g.Critical, status.Unknown: g.Unknown, status.Warning: g.Warning, status.OK: g.OK}}
		if g.WorstProvenVM != nil && g.WorstProvenTakenAt != nil {
			vm, err := s.db.VM(ctx, *g.WorstProvenVM)
			if err != nil {
				return 0, nil, err
			}
			cg.WorstRPO = &FleetWorst{VMID: vm.ID, VM: vm.Name, ProvenRPOS: now.Sub(timeOf(*g.WorstProvenTakenAt)).Seconds(), RPOTargetS: g.WorstRPOTargetS}
		}
		if g.OldestProofAt != nil {
			a := now.Sub(timeOf(*g.OldestProofAt)).Seconds()
			cg.OldestProofS = &a
		}
		out.Groups = append(out.Groups, cg)
	}
	if off+n < len(shown) {
		out.Next = strconv.Itoa(off + n)
	}
	return http.StatusOK, out, nil
}

// VMListView is the VM table: filtered, worst first then oldest proof, in pages.
type VMListView struct {
	Clock   ServerClock `json:"clock"`
	WindowS int64       `json:"window_s"`
	Total   int         `json:"total"` // VMs that match the filter
	Rows    []VMListRow `json:"rows"`
	Next    string      `json:"next"` // pass as after= for the next page; empty on the last
}

// VMListRow is one VM: where it is, its status and why, the point it can return to, the figures against their
// targets, and its last 24 hours.
type VMListRow struct {
	VMID            int64           `json:"vm_id"`
	VM              string          `json:"vm"`
	HostID          int64           `json:"host_id"`
	Host            string          `json:"host"`
	PolicyID        *int64          `json:"policy_id"`
	Policy          string          `json:"policy"`
	Tags            []string        `json:"tags"`
	Status          string          `json:"status"`
	Since           string          `json:"since"`
	Code            string          `json:"code"`
	Scope           *status.Scope   `json:"scope"`
	Reason          string          `json:"reason"`
	Notes           []status.Reason `json:"notes"`
	Proven          *Proven         `json:"proven"`
	Targets         *Targets        `json:"targets"`
	TheoreticalRPOS *float64        `json:"theoretical_rpo_s"`
	ProvenRPOS      *float64        `json:"proven_rpo_s"`
	ProvenRTOS      *float64        `json:"proven_rto_s"`
	ProofAgeS       *float64        `json:"proof_age_s"`
	Events          []TimelineEvent `json:"events"`
}

func (s *Server) vmListView(c *call) (int, any, error) {
	ctx := c.r.Context()
	now := s.now()
	q := c.r.URL.Query()
	f := store.VMFilter{Attention: q.Get("attention") == "true", Tag: q.Get("tag"), Code: q.Get("code"), ScopeKind: q.Get("scope_kind"),
		Q: q.Get("q"), Sort: q.Get("sort"), After: q.Get("after")}
	for _, w := range strings.Split(q.Get("status"), ",") {
		if w = strings.ToUpper(strings.TrimSpace(w)); w != "" {
			if w != status.OK && w != status.Warning && w != status.Unknown && w != status.Critical {
				return 0, nil, fmt.Errorf("%w: status %q (OK, WARNING, UNKNOWN, CRITICAL, comma-separated)", store.ErrInvalid, w)
			}
			f.Ranks = append(f.Ranks, store.RankOf(w))
		}
	}
	var err error
	if f.HostID, err = c.intQuery("host_id", 0, 0); err != nil {
		return 0, nil, err
	}
	if f.PolicyID, err = c.intQuery("policy_id", 0, 0); err != nil {
		return 0, nil, err
	}
	if f.ScopeID, err = c.intQuery("scope_id", 0, 0); err != nil {
		return 0, nil, err
	}
	n, err := c.intQuery("limit", 50, 200)
	if err != nil {
		return 0, nil, err
	}
	f.Limit = int(n)
	rows, total, err := s.db.ListVMs(ctx, f)
	if err != nil {
		return 0, nil, err
	}
	out := VMListView{Clock: serverClock(now), WindowS: int64(listWindow / time.Second), Total: total, Rows: []VMListRow{}}
	for _, r := range rows {
		row, err := s.vmListRow(ctx, r, now)
		if err != nil {
			return 0, nil, fmt.Errorf("VM %s: %w", r.Name, err)
		}
		out.Rows = append(out.Rows, row)
	}
	if len(rows) == f.Limit {
		out.Next = rows[len(rows)-1].Cursor
	}
	return http.StatusOK, out, nil
}

func (s *Server) vmListRow(ctx context.Context, r store.VMRow, now time.Time) (VMListRow, error) {
	row := VMListRow{VMID: r.ID, VM: r.Name, HostID: r.HostID, Host: r.Host, PolicyID: r.PolicyID, Policy: r.Policy, Tags: r.Tags,
		Status: r.Status.Status, Code: r.Status.Code, Notes: []status.Reason{}}
	st := r.Status
	if st.Since != nil {
		row.Since = timeOf(*st.Since).Format(time.RFC3339Nano)
	}
	if st.ScopeKind != "" {
		row.Scope = &status.Scope{Kind: st.ScopeKind, ID: st.ScopeID, Name: st.ScopeName}
	}
	if st.VMID == 0 { // never computed
		row.Reason = "not computed yet: the status is computed at the scheduler's next tick"
	} else {
		res, err := renderStatus(st, now)
		if err != nil {
			return row, err
		}
		row.Reason, row.Notes = res.Reasons[0].Text, res.NoteReasons
		row.TheoreticalRPOS, row.ProvenRPOS, row.ProvenRTOS, row.ProofAgeS = res.TheoreticalRPO, res.ProvenRPO, res.ProvenRTO, res.ProofAge
	}
	if st.RPOTargetS != nil && st.RTOTargetS != nil && st.MaxProofAgeS != nil {
		row.Targets = &Targets{RPOS: *st.RPOTargetS, RTOS: *st.RTOTargetS, MaxProofAgeS: *st.MaxProofAgeS}
	}
	if st.ProvenPointID != nil && st.ProvenTakenAt != nil && st.ProvenTestID != nil && st.ProvenFinishedAt != nil {
		row.Proven = &Proven{PointID: *st.ProvenPointID, PointTakenAt: timeOf(*st.ProvenTakenAt).Format(time.RFC3339Nano),
			TestID: *st.ProvenTestID, TestFinishedAt: timeOf(*st.ProvenFinishedAt).Format(time.RFC3339Nano)}
	}
	var err error
	row.Events, err = s.timelineSince(ctx, r.ID, now, listWindow)
	return row, err
}

// SearchView is what a text finds, for the global search box (T27): VMs, hosts and policies by name, a point by id.
type SearchView struct {
	Q string `json:"q"`
	store.SearchHits
}

func (s *Server) searchView(c *call) (int, any, error) {
	n, err := c.intQuery("limit", 8, 50)
	if err != nil {
		return 0, nil, err
	}
	q := c.r.URL.Query().Get("q")
	if len(q) > 200 {
		return 0, nil, fmt.Errorf("%w: q is at most 200 characters", store.ErrInvalid)
	}
	h, err := s.db.Search(c.r.Context(), q, int(n))
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, SearchView{Q: q, SearchHits: h}, nil
}

// TagsInput replaces a VM's tags.
type TagsInput struct {
	Tags []string `json:"tags"` // "key" or "key:value"
}

func (s *Server) setTags(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	var in TagsInput
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	tags, err := s.db.SetTags(c.r.Context(), id, in.Tags)
	if err != nil {
		return 0, nil, err
	}
	c.detail = "tags: " + strings.Join(tags, " ")
	return http.StatusOK, tags, nil
}

// ReasonCode is one code of the closed list (docs/reason-codes.md), for clients.
type ReasonCode struct {
	Code   string   `json:"code"`
	Status string   `json:"status"` // empty for a note
	Rule   string   `json:"rule"`
	Scope  string   `json:"scope"`
	Params []string `json:"params"`
	Cause  string   `json:"cause"`
	Means  string   `json:"means"`
}

// ReasonCodes is the list and its version.
type ReasonCodes struct {
	Version int          `json:"version"`
	Codes   []ReasonCode `json:"codes"`
}

func reasonCodes(*call) (int, any, error) {
	out := ReasonCodes{Version: status.CodesVersion, Codes: []ReasonCode{}}
	for _, d := range status.Defs() {
		ps := d.Params
		if ps == nil {
			ps = []string{}
		}
		out.Codes = append(out.Codes, ReasonCode{Code: string(d.Code), Status: d.Status, Rule: d.Rule, Scope: d.Scope, Params: ps, Cause: d.Cause, Means: d.Means})
	}
	return http.StatusOK, out, nil
}

// offsetPage reads after= (an offset, from a previous answer's next) and limit=.
func offsetPage(c *call, def int64) (int, int, error) {
	off, err := c.intQuery("after", 0, 0)
	if err != nil {
		return 0, 0, err
	}
	n, err := c.intQuery("limit", def, 200)
	if err != nil {
		return 0, 0, err
	}
	if n == 0 {
		n = def
	}
	return int(off), int(n), nil
}
