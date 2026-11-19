package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Views (T20): what the web UI's screens show, each in one request. They read what the API's resources read; nothing
// here changes state. Design: docs/design/t20-direction.md.

// historyWindow is how far back the history strip of a VM reaches.
const historyWindow = 6 * time.Hour

// ServerClock is the server's time and time zone: the UI shows every time in the server's local time (PO decision
// 2026-11-17), with the zone named once.
type ServerClock struct {
	Now     string `json:"now"`
	Zone    string `json:"zone"`     // IANA name when known (Europe/Istanbul), else the abbreviation
	OffsetS int    `json:"offset_s"` // seconds east of UTC, now
}

func serverClock(now time.Time) ServerClock {
	abbr, off := now.Zone()
	name := os.Getenv("TZ")
	if name == "" {
		if l, err := os.Readlink("/etc/localtime"); err == nil {
			if _, after, ok := strings.Cut(l, "zoneinfo/"); ok {
				name = after
			}
		}
	}
	if name == "" {
		name = abbr
	}
	return ServerClock{Now: now.UTC().Format(time.RFC3339Nano), Zone: name, OffsetS: off}
}

// Proven is the point a VM can return to and the test that proved it.
type Proven struct {
	PointID        int64  `json:"point_id"`
	PointTakenAt   string `json:"point_taken_at"`
	TestID         int64  `json:"test_id"`
	TestFinishedAt string `json:"test_finished_at"`
	BundleID       string `json:"bundle_id"`
}

// Targets are a policy's targets.
type Targets struct {
	RPOS         int64 `json:"rpo_target_s"`
	RTOS         int64 `json:"rto_target_s"`
	MaxProofAgeS int64 `json:"max_proof_age_s"`
}

// TimelineEvent is one mark on a VM's history strip.
type TimelineEvent struct {
	At   string `json:"at"`
	Kind string `json:"kind"` // backup, damaged, backup_failed, test_ok, test_warning, test_failed, test_unknown
	ID   int64  `json:"id"`   // the point, the job or the test
}

// VMOverview is one row of the overview.
type VMOverview struct {
	VMStatus
	Host    string          `json:"host"`
	Policy  string          `json:"policy"` // "" without a policy
	Targets *Targets        `json:"targets"`
	Proven  *Proven         `json:"proven"`
	Events  []TimelineEvent `json:"events"`
	// Activity is what the server does for the VM now: its queued and running jobs, oldest first.
	Activity []store.Job `json:"activity"`
}

// OverviewView is the overview screen: every VM, worst first, and the installation's own state.
type OverviewView struct {
	Clock         ServerClock    `json:"clock"`
	WindowS       int64          `json:"window_s"`
	VMs           []VMOverview   `json:"vms"`       // a page, worst first then oldest proof
	Total         int            `json:"total"`     // every VM
	ByStatus      map[string]int `json:"by_status"` // every VM by status
	Next          string         `json:"next"`      // after= for the next page; empty on the last
	Ledger        LedgerSummary  `json:"ledger"`
	RecoveryKit   KitSummary     `json:"recovery_kit"`
	Notifications NotifySummary  `json:"notifications"`
}

var statusRank = map[string]int{"CRITICAL": 0, "UNKNOWN": 1, "WARNING": 2, "OK": 3}

func (s *Server) overviewView(c *call) (int, any, error) {
	ctx := c.r.Context()
	now := s.now()
	n, err := c.intQuery("limit", 500, 500)
	if err != nil {
		return 0, nil, err
	}
	// From the status table, worst first then oldest proof, in pages (T24): the T20 screen reads the first page.
	rows, total, err := s.db.ListVMs(ctx, store.VMFilter{After: c.r.URL.Query().Get("after"), Limit: int(n)})
	if err != nil {
		return 0, nil, err
	}
	fc, err := s.db.FleetCounts(ctx)
	if err != nil {
		return 0, nil, err
	}
	out := OverviewView{Clock: serverClock(now), WindowS: int64(historyWindow / time.Second), VMs: []VMOverview{}, Total: total,
		ByStatus: map[string]int{status.Critical: fc.Critical, status.Unknown: fc.Unknown, status.Warning: fc.Warning, status.OK: fc.OK}}
	for _, r := range rows {
		row, err := s.overviewRow(ctx, r, now)
		if err != nil {
			return 0, nil, fmt.Errorf("VM %s: %w", r.Name, err)
		}
		out.VMs = append(out.VMs, row)
	}
	if len(rows) == int(n) {
		out.Next = rows[len(rows)-1].Cursor
	}
	if out.Ledger, err = s.ledgerSummary(ctx); err != nil {
		return 0, nil, err
	}
	if out.RecoveryKit, err = kitSummary(ctx, s.cfg, s.db); err != nil {
		return 0, nil, err
	}
	if out.Notifications, err = s.notifySummary(ctx); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, out, nil
}

// overviewRow is one VM of the overview, from its status table row.
func (s *Server) overviewRow(ctx context.Context, r store.VMRow, now time.Time) (VMOverview, error) {
	lr, err := s.vmListRow(ctx, r, now)
	if err != nil {
		return VMOverview{}, err
	}
	res := status.Result{Status: lr.Status, Reasons: []status.Reason{}, Notes: []string{}, NoteReasons: []status.Reason{}}
	if r.Status.VMID != 0 {
		if res, err = renderStatus(r.Status, now); err != nil {
			return VMOverview{}, err
		}
	} else {
		res.Reasons = append(res.Reasons, status.Reason{Status: status.Unknown, Code: status.NoRule, Text: lr.Reason})
	}
	row := VMOverview{VMStatus: VMStatus{VMID: r.ID, VM: r.Name, ChangedAt: r.Status.ChangedAt, Result: res}, Host: r.Host, Policy: r.Policy,
		Targets: lr.Targets, Proven: lr.Proven, Events: []TimelineEvent{}}
	if row.Events, err = s.timeline(ctx, r.ID, now); err != nil {
		return row, err
	}
	if row.Activity, err = s.db.OpenJobs(ctx, r.ID); err != nil {
		return row, err
	}
	return row, nil
}

func (s *Server) vmOverview(ctx context.Context, vm store.VM, now time.Time, hosts map[int64]string) (VMOverview, error) {
	st, err := s.vmStatus(ctx, vm)
	if err != nil {
		return VMOverview{}, err
	}
	row := VMOverview{VMStatus: st, Events: []TimelineEvent{}}
	if name, ok := hosts[vm.HostID]; ok {
		row.Host = name
	} else {
		h, err := s.db.Host(ctx, vm.HostID)
		if err != nil {
			return row, err
		}
		hosts[vm.HostID], row.Host = h.Name, h.Name
	}
	f, err := s.db.StatusFacts(ctx, vm.ID)
	if err != nil {
		return row, err
	}
	if f.Policy != nil {
		row.Policy = f.Policy.Name
		row.Targets = &Targets{RPOS: f.Policy.RPOTargetS, RTOS: f.Policy.RTOTargetS, MaxProofAgeS: f.Policy.MaxProofAgeS}
	}
	if f.Passing != nil {
		row.Proven = &Proven{PointID: f.PassingPoint.ID, PointTakenAt: f.PassingPoint.TakenAt, TestID: f.Passing.ID,
			TestFinishedAt: f.Passing.FinishedAt, BundleID: f.Passing.BundleID}
	}
	if row.Events, err = s.timeline(ctx, vm.ID, now); err != nil {
		return row, err
	}
	if row.Activity, err = s.db.OpenJobs(ctx, vm.ID); err != nil {
		return row, err
	}
	return row, nil
}

// timeline is the VM's history strip: the last historyWindow of points, tests and failed backups, oldest first.
func (s *Server) timeline(ctx context.Context, vmID int64, now time.Time) ([]TimelineEvent, error) {
	return s.timelineSince(ctx, vmID, now, historyWindow)
}

// timelineSince is the history strip of the last window.
func (s *Server) timelineSince(ctx context.Context, vmID int64, now time.Time, window time.Duration) ([]TimelineEvent, error) {
	since := now.Add(-window)
	tl, err := s.db.VMTimeline(ctx, vmID, since)
	if err != nil {
		return nil, err
	}
	out := []TimelineEvent{}
	in := func(at string) bool {
		t, err := time.Parse(time.RFC3339Nano, at)
		return err == nil && !t.Before(since) && !t.After(now)
	}
	for _, p := range tl.Points {
		if !in(p.TakenAt) {
			continue
		}
		kind := "backup"
		if p.CheckState == "failed" {
			kind = "damaged"
		}
		out = append(out, TimelineEvent{At: p.TakenAt, Kind: kind, ID: p.ID})
	}
	for _, t := range tl.Tests {
		if !in(t.FinishedAt) {
			continue
		}
		kind := map[string]string{"OK": "test_ok", "WARNING": "test_warning", "CRITICAL": "test_failed"}[t.Verdict]
		if kind == "" {
			kind = "test_unknown"
		}
		out = append(out, TimelineEvent{At: t.FinishedAt, Kind: kind, ID: t.ID})
	}
	for _, j := range tl.FailedJobs {
		if j.Kind == "backup" && in(j.FinishedAt) {
			out = append(out, TimelineEvent{At: j.FinishedAt, Kind: "backup_failed", ID: j.ID})
		}
	}
	slices.SortStableFunc(out, func(a, b TimelineEvent) int { return strings.Compare(a.At, b.At) })
	return out, nil
}

// --- one VM ---

// TestView is a restore test with what the VM page shows of its evidence.
type TestView struct {
	store.RestoreTest
	EvidenceID  int64    `json:"evidence_id"` // the catalog's id of its bundle (0: none), for /evidence/{id}/files
	LedgerSeq   int      `json:"ledger_seq"`  // 0: no bundle
	Evidence    string   `json:"evidence"`    // verified, checked, not_checked, none, invalid
	Screenshots []string `json:"screenshots"` // file names in the bundle
}

// BundleCheck is one bundle verified now against the installation's key.
type BundleCheck struct {
	BundleID string   `json:"bundle_id"`
	Verdict  string   `json:"verdict"`
	Files    int      `json:"files"`
	KeyID    string   `json:"key_id"`
	Problems []string `json:"problems"`
}

// ReportHead is a daily report's ledger head (no body).
type ReportHead struct {
	ID          int64  `json:"id"`
	GeneratedAt string `json:"generated_at"`
	HeadSeq     int    `json:"head_seq"`
	HeadSHA256  string `json:"head_sha256"`
}

// VMView is one VM's page.
type VMView struct {
	Clock         ServerClock         `json:"clock"`
	WindowS       int64               `json:"window_s"`
	VM            store.VM            `json:"vm"`
	Host          store.Host          `json:"host"`
	Policy        *store.Policy       `json:"policy"`
	Repository    *store.Repository   `json:"repository"`
	Status        VMOverview          `json:"status"`
	StatusHistory []store.StateChange `json:"status_history"`
	Points        []store.Point       `json:"points"` // present points, newest first
	PointsTotal   int                 `json:"points_total"`
	Tests         []TestView          `json:"tests"` // the newest 12, then the older ones that did not pass
	LatestBundle  *BundleCheck        `json:"latest_bundle"`
	Ledger        LedgerSummary       `json:"ledger"`
	DailyReport   *ReportHead         `json:"daily_report"`
}

func (s *Server) vmView(c *call) (int, any, error) {
	ctx := c.r.Context()
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	vm, err := s.db.VM(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	now := s.now()
	v := VMView{Clock: serverClock(now), WindowS: int64(historyWindow / time.Second), VM: vm}
	if v.Status, err = s.vmOverview(ctx, vm, now, map[int64]string{}); err != nil {
		return 0, nil, err
	}
	if v.Host, err = s.db.Host(ctx, vm.HostID); err != nil {
		return 0, nil, err
	}
	if vm.PolicyID != nil {
		p, err := s.db.Policy(ctx, *vm.PolicyID)
		if err != nil {
			return 0, nil, err
		}
		v.Policy = &p
		r, err := s.db.Repository(ctx, p.RepositoryID)
		if err != nil {
			return 0, nil, err
		}
		v.Repository = &r
	}
	if v.StatusHistory, err = s.db.StateChanges(ctx, "vm_status", vm.ID, 0, 1000); err != nil {
		return 0, nil, err
	}
	all, err := s.db.Points(ctx, vm.ID, 1000)
	if err != nil {
		return 0, nil, err
	}
	v.PointsTotal, v.Points = len(all), []store.Point{}
	for _, p := range all {
		if p.State == "present" {
			v.Points = append(v.Points, p)
		}
	}
	tests, err := s.db.RestoreTests(ctx, vm.ID, 500)
	if err != nil {
		return 0, nil, err
	}
	ev, err := s.db.EvidenceBundles(ctx, vm.ID, 1000)
	if err != nil {
		return 0, nil, err
	}
	seq, eid := map[string]int{}, map[string]int64{}
	for _, e := range ev {
		seq[e.BundleID], eid[e.BundleID] = e.LedgerSeq, e.ID
	}
	if v.Ledger, err = s.ledgerSummary(ctx); err != nil {
		return 0, nil, err
	}
	v.Tests = []TestView{}
	for i, t := range tests {
		if i >= 12 && t.Verdict == "OK" {
			continue
		}
		tv := TestView{RestoreTest: t, EvidenceID: eid[t.BundleID], LedgerSeq: seq[t.BundleID], Evidence: "none", Screenshots: []string{}}
		switch {
		case t.BundleID == "":
		case v.Ledger.Verdict == evidence.OK && tv.LedgerSeq > 0 && tv.LedgerSeq <= v.Ledger.HeadSeq:
			tv.Evidence = "checked"
		default:
			tv.Evidence = "not_checked"
		}
		if t.BundleID != "" {
			tv.Screenshots = s.screenshots(t.BundleID)
		}
		v.Tests = append(v.Tests, tv)
	}
	// The newest test's bundle is verified now: the evidence a page shows first is never taken on trust.
	if len(v.Tests) > 0 && v.Tests[0].BundleID != "" {
		bc, err := s.checkBundle(v.Tests[0].BundleID)
		if err != nil {
			return 0, nil, err
		}
		v.LatestBundle = &bc
		v.Tests[0].Evidence = map[bool]string{true: "verified", false: "invalid"}[bc.Verdict == evidence.OK]
	}
	if rs, err := s.db.DailyReports(ctx, 1); err != nil {
		return 0, nil, err
	} else if len(rs) == 1 {
		v.DailyReport = &ReportHead{ID: rs[0].ID, GeneratedAt: rs[0].GeneratedAt, HeadSeq: rs[0].HeadSeq, HeadSHA256: rs[0].HeadSHA256}
	}
	return http.StatusOK, v, nil
}

// screenshots lists a bundle's screenshot files (screenshot-*.png), in name order.
func (s *Server) screenshots(bundleID string) []string {
	out := []string{}
	des, err := os.ReadDir(filepath.Join(s.cfg.bundlesDir(), bundleID))
	if err != nil {
		return out
	}
	for _, d := range des {
		if d.Type().IsRegular() && strings.HasPrefix(d.Name(), "screenshot-") && strings.HasSuffix(d.Name(), ".png") {
			out = append(out, d.Name())
		}
	}
	return out
}

// checkBundle verifies one bundle against the installation's key (evidence spec §8.4).
func (s *Server) checkBundle(bundleID string) (BundleCheck, error) {
	pub, err := evidence.LoadPublicKey(s.cfg.pubPath())
	if err != nil {
		return BundleCheck{}, fmt.Errorf("the evidence key: %w", err)
	}
	r := evidence.VerifyBundle(filepath.Join(s.cfg.bundlesDir(), bundleID), pub)
	return BundleCheck{BundleID: bundleID, Verdict: r.Verdict, Files: r.Files, KeyID: r.KeyID, Problems: r.Problems}, nil
}

// --- evidence files ---

// bundleOf loads the evidence bundle named by the path's {id} (the catalog's id).
func (s *Server) bundleOf(c *call) (store.EvidenceBundle, error) {
	id, err := c.id("id")
	if err != nil {
		return store.EvidenceBundle{}, err
	}
	return s.db.Evidence(c.r.Context(), id)
}

// evidenceFile answers one regular file of a bundle (a screenshot, the serial log), never anything outside it.
func (s *Server) evidenceFile(c *call) (int, any, error) {
	e, err := s.bundleOf(c)
	if err != nil {
		return 0, nil, err
	}
	name := c.r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return 0, nil, fmt.Errorf("%w: file name %q", store.ErrInvalid, name)
	}
	p := filepath.Join(s.cfg.bundlesDir(), e.BundleID, name)
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !fi.Mode().IsRegular()) {
		return 0, nil, fmt.Errorf("%w: bundle %s has no file %s", store.ErrNotFound, e.BundleID, name)
	}
	if err != nil {
		return 0, nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, nil, err
	}
	ct := map[string]string{".png": "image/png", ".json": "application/json", ".log": "text/plain; charset=utf-8",
		".txt": "text/plain; charset=utf-8", ".xml": "text/plain; charset=utf-8", ".leases": "text/plain; charset=utf-8",
		".sig": "text/plain; charset=utf-8"}[filepath.Ext(name)]
	if ct == "" {
		ct = "application/octet-stream"
	}
	c.raw = true
	c.w.Header().Set("Content-Type", ct)
	c.w.Header().Set("Cache-Control", "private, max-age=86400, immutable") // a sealed bundle does not change
	c.w.Header().Set("X-Content-Type-Options", "nosniff")
	c.w.WriteHeader(http.StatusOK)
	_, err = c.w.Write(b)
	return http.StatusOK, nil, err
}

func (s *Server) verifyBundle(c *call) (int, any, error) {
	e, err := s.bundleOf(c)
	if err != nil {
		return 0, nil, err
	}
	return one(s.checkBundle(e.BundleID))
}
