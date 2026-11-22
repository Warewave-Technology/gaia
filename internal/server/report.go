package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// The daily report (ADR-007 §10) says, once a day, where every VM stands and what the evidence ledger's head is. Its
// copies, mailed and posted elsewhere, are the external anchor of the ledger: the head statement in it (evidence spec
// §6.8) lets anyone detect a ledger cut off or rewritten after it (gaia evidence verify -head).

// DailyReportFormat identifies the report's JSON.
const DailyReportFormat = "gaia-daily-report"

// DailyReportDoc is the daily report.
type DailyReportDoc struct {
	Format        string            `json:"format"`
	FormatVersion int               `json:"format_version"`
	ID            int64             `json:"id"`
	Installation  string            `json:"installation"`
	Version       string            `json:"version"`
	EvidenceKeyID string            `json:"evidence_key_id"`
	GeneratedAt   string            `json:"generated_at"`
	Reason        string            `json:"reason"`
	Summary       map[string]int    `json:"summary"` // VMs per status
	VMs           []ReportVM        `json:"vms"`
	LedgerCheck   store.LedgerCheck `json:"ledger_check"`
	LedgerHead    json.RawMessage   `json:"ledger_head"` // the signed head statement; null when there is none
	RecoveryKit   KitSummary        `json:"recovery_kit"`
	LastRebuild   *ReportRebuild    `json:"last_rebuild"` // the newest rebuild from a recovery kit (restore-state); null: never
	Notifications NotifySummary     `json:"notifications"`
	Security      ReportSecurity    `json:"security"` // sign-in and security since the previous report (ADR-008 §1, §10)
	HowToVerify   string            `json:"how_to_verify"`
	Problems      []string          `json:"problems"` // what the report could not say
}

// ReportSecurity is what changed in sign-in and security since the previous report: settings, break-glass and
// new-device sign-ins, paused accounts, factor resets, role and token changes; and each API token's use.
type ReportSecurity struct {
	Since     string              `json:"since"` // the previous report ("" = the first report)
	Changes   []store.StateChange `json:"changes"`
	TokenUses []ReportTokenUse    `json:"api_token_uses"`
}

// ReportTokenUse is an API token's use on its last day of use.
type ReportTokenUse struct {
	Name  string `json:"name"`
	Day   string `json:"day"`
	Count int    `json:"count"`
}

// ReportRebuild is a rebuild of the server from its recovery kit and a state backup (install.md step 13).
type ReportRebuild struct {
	At                 string `json:"at"`
	StateBackup        string `json:"state_backup"`
	StateBackupCreated string `json:"state_backup_created"`
	KitGeneration      int64  `json:"kit_generation"`
}

// rebuildShown is how long the report's text names a rebuild: the readers of the first reports after it.
const rebuildShown = 48 * time.Hour

// ReportVM is one VM's line in the report.
type ReportVM struct {
	VM             string        `json:"vm"`
	Status         string        `json:"status"`
	Since          string        `json:"since"`
	Reason         string        `json:"reason" deprecated:"kept for M2 scripts; the UI builds its sentence from code, scope and params (ADR-007 C11)"`
	Code           string        `json:"code"`  // the deciding reason's code (docs/reason-codes.md)
	Scope          *status.Scope `json:"scope"` // its shared cause, if any
	Codes          []string      `json:"codes"` // every reason's code, worst first
	TheoreticalRPO *float64      `json:"theoretical_rpo_s"`
	ProvenRPO      *float64      `json:"proven_rpo_s"`
	ProvenRTO      *float64      `json:"proven_rto_s"`
	ProofAge       *float64      `json:"proof_age_s"`
}

// NotifySummary is the state of the notification channels.
type NotifySummary struct {
	State    string   `json:"state"` // OK, WARNING
	Channels int      `json:"channels"`
	Enabled  int      `json:"enabled"`
	Text     string   `json:"text"`
	Failing  []string `json:"failing"`
}

// LedgerSummary is the latest ledger check.
type LedgerSummary struct {
	store.LedgerCheck
	Unchecked int    `json:"unchecked"` // entries sealed after this check: not vouched for until the next one
	Text      string `json:"text"`
}

const howToVerify = `Keep this report. To check the evidence ledger later against the head recorded here, with the installation's ` +
	`evidence public key (the trust anchor) and an export of the ledger and bundles:  gaia evidence verify -pubkey anchor.pub ` +
	`-ledger ledger.jsonl -evidence-dir bundles -head THIS-REPORT.json  -- a ledger cut off or rewritten after this report is CRITICAL ` +
	`(ledger_truncated, head_mismatch).`

func loadPub(c Config) (string, error) {
	pub, err := evidence.LoadPublicKey(c.pubPath())
	if err != nil {
		return "", err
	}
	return evidence.KeyID(pub), nil
}

// checkLedger verifies the evidence ledger and every bundle against the installation's key, the head of the latest
// daily report, and the catalog (which knows the highest entry a bundle got), and records the result.
func (s *Server) checkLedger(ctx context.Context, actor, reason string) (store.LedgerCheck, error) {
	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()
	c := store.LedgerCheck{Reason: reason, Problems: []string{}}
	pub, err := evidence.LoadPublicKey(s.cfg.pubPath())
	if err != nil {
		return c, fmt.Errorf("the evidence key: %w", err)
	}
	var heads [][]byte
	if rs, err := s.db.DailyReports(ctx, 1); err != nil {
		return c, err
	} else if len(rs) == 1 && string(rs[0].LedgerHead) != "null" {
		heads = append(heads, rs[0].LedgerHead)
	}
	catalog, err := s.db.MaxEvidenceLedgerSeq(ctx)
	if err != nil {
		return c, err
	}
	_, statErr := os.Stat(s.cfg.ledgerPath())
	switch {
	case errors.Is(statErr, fs.ErrNotExist) && catalog == 0 && len(heads) == 0:
		c.Verdict = evidence.OK // nothing was ever sealed: there is nothing to check yet
		c.Reason += " (no evidence yet)"
	default:
		r, err := evidence.VerifyLedgerHeads(s.cfg.ledgerPath(), s.cfg.bundlesDir(), pub, heads)
		if err != nil {
			c.Verdict = evidence.Unknown
			c.Problems = append(c.Problems, "the ledger cannot be read: "+err.Error())
			break
		}
		c.Verdict, c.Entries = r.Verdict, r.Entries
		for _, f := range r.AllFindings() {
			c.Problems = append(c.Problems, strings.TrimPrefix(fmt.Sprintf("%s: %s: %s", f.Where, f.Code, f.Detail), ": "))
		}
		if catalog > r.Entries {
			c.Verdict = evidence.Critical
			c.Problems = append(c.Problems, fmt.Sprintf("ledger_truncated: the catalog records a bundle at entry %d, the ledger has %d entries", catalog, r.Entries))
		}
		if r.Entries > 0 {
			c.HeadSeq, c.HeadSHA256 = r.Entries, lastEntryHash(s.cfg.ledgerPath())
		}
	}
	return s.db.AddLedgerCheck(context.WithoutCancel(ctx), actor, c)
}

// lastEntryHash is the entry hash of the ledger's last line ("" if it cannot be read).
func lastEntryHash(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	l := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	var w struct {
		Payload string `json:"payload"`
	}
	if json.Unmarshal([]byte(l[len(l)-1]), &w) != nil {
		return ""
	}
	h := sha256.Sum256([]byte(w.Payload))
	return hex.EncodeToString(h[:])
}

// dailyReportIfDue makes the day's report once daily_report_at has passed.
func (s *Server) dailyReportIfDue(ctx context.Context, now time.Time) error {
	at := nextRun(now.Add(-24*time.Hour), s.cfg.DailyReportAt)
	if now.Before(at) {
		return nil
	}
	rs, err := s.db.DailyReports(ctx, 1)
	if err != nil {
		return err
	}
	if len(rs) == 1 {
		t, err := time.Parse(time.RFC3339Nano, rs[0].GeneratedAt)
		if err != nil {
			return err
		}
		if !t.Before(at) {
			return nil
		}
	}
	_, err = s.makeDailyReport(ctx, "system", "schedule: daily at "+s.cfg.DailyReportAt)
	return err
}

// makeDailyReport checks the ledger, signs its head, gathers every VM's status, records the report and queues it for
// every channel that takes daily reports.
func (s *Server) makeDailyReport(ctx context.Context, actor, reason string) (store.DailyReport, error) {
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	lc, err := s.checkLedger(ctx, actor, "daily report")
	if err != nil {
		return store.DailyReport{}, fmt.Errorf("ledger check: %w", err)
	}
	host, _ := os.Hostname()
	now := time.Now().UTC()
	doc := DailyReportDoc{Format: DailyReportFormat, FormatVersion: 1, Installation: host, Version: s.version, GeneratedAt: now.Format(time.RFC3339Nano),
		Reason: reason, Summary: map[string]int{}, VMs: []ReportVM{}, LedgerCheck: lc, LedgerHead: json.RawMessage("null"), HowToVerify: howToVerify,
		Problems: []string{}}
	if doc.EvidenceKeyID, err = loadPub(s.cfg); err != nil {
		return store.DailyReport{}, err
	}
	var head evidence.Head
	if lc.Entries > 0 {
		key, err := evidence.LoadPrivateKey(s.cfg.keyPath())
		if err != nil {
			return store.DailyReport{}, err
		}
		h, b, err := evidence.HeadStatement(s.cfg.ledgerPath(), key, now)
		if err != nil {
			doc.Problems = append(doc.Problems, "no ledger head: "+err.Error())
		} else {
			head, doc.LedgerHead = h, b
		}
	} else if lc.Verdict == evidence.OK {
		doc.Problems = append(doc.Problems, "no ledger head: no evidence was sealed yet")
	}
	if lc.Verdict != evidence.OK {
		doc.Problems = append(doc.Problems, fmt.Sprintf("the evidence ledger is %s: %s", lc.Verdict, strings.Join(lc.Problems, "; ")))
	}
	vms, err := s.db.VMs(ctx, 0)
	if err != nil {
		return store.DailyReport{}, err
	}
	for _, vm := range vms {
		st, err := s.vmStatus(ctx, vm)
		if err != nil {
			doc.Problems = append(doc.Problems, fmt.Sprintf("VM %s: status: %v", vm.Name, err))
			doc.VMs = append(doc.VMs, ReportVM{VM: vm.Name, Status: status.Unknown, Reason: err.Error()})
			doc.Summary[status.Unknown]++
			continue
		}
		codes := []string{}
		for _, x := range st.Reasons {
			codes = append(codes, string(x.Code))
		}
		doc.VMs = append(doc.VMs, ReportVM{VM: vm.Name, Status: st.Status, Since: st.ChangedAt, Reason: st.Reasons[0].Text,
			Code: string(st.Reasons[0].Code), Scope: st.Reasons[0].Scope, Codes: codes,
			TheoreticalRPO: st.TheoreticalRPO, ProvenRPO: st.ProvenRPO, ProvenRTO: st.ProvenRTO, ProofAge: st.ProofAge})
		doc.Summary[st.Status]++
	}
	if doc.RecoveryKit, err = kitSummary(ctx, s.cfg, s.db); err != nil {
		return store.DailyReport{}, err
	}
	if doc.Notifications, err = s.notifySummary(ctx); err != nil {
		return store.DailyReport{}, err
	}
	if doc.Security.Since, err = s.db.LastDailyReportAt(ctx); err != nil {
		return store.DailyReport{}, err
	}
	if doc.Security.Changes, err = s.db.SecurityChanges(ctx, doc.Security.Since); err != nil {
		return store.DailyReport{}, err
	}
	toks, err := s.db.APITokens(ctx)
	if err != nil {
		return store.DailyReport{}, err
	}
	doc.Security.TokenUses = []ReportTokenUse{}
	for _, t := range toks {
		if t.UseDay != "" && t.RevokedAt == "" {
			doc.Security.TokenUses = append(doc.Security.TokenUses, ReportTokenUse{Name: t.Name, Day: t.UseDay, Count: t.UseCount})
		}
	}
	rs, err := s.db.StateRestores(ctx)
	if err != nil {
		return store.DailyReport{}, err
	}
	if len(rs) > 0 {
		doc.LastRebuild = &ReportRebuild{At: rs[0].RestoredAt, StateBackup: rs[0].StateBackup, StateBackupCreated: rs[0].StateBackupCreated,
			KitGeneration: rs[0].KitGeneration}
	}
	if doc.ID, err = s.db.NextDailyReportID(ctx); err != nil {
		return store.DailyReport{}, err
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return store.DailyReport{}, err
	}
	ev := s.event(evDaily, doc.GeneratedAt)
	ev.Subject = fmt.Sprintf("Gaia %s: daily report %s: %s", host, now.Format("2006-01-02"), summaryText(doc.Summary))
	ev.Text = reportText(doc)
	ev.LedgerHead, ev.Report = doc.LedgerHead, b
	body, err := json.Marshal(ev)
	if err != nil {
		return store.DailyReport{}, err
	}
	return s.db.AddDailyReport(context.WithoutCancel(ctx), store.DailyReport{ID: doc.ID, GeneratedAt: doc.GeneratedAt, Reason: reason,
		HeadSeq: head.Seq, HeadSHA256: head.EntrySHA256, LedgerHead: doc.LedgerHead, Report: b}, ev.Subject, body)
}

func summaryText(m map[string]int) string {
	if len(m) == 0 {
		return "no VMs"
	}
	var parts []string
	for _, st := range []string{status.Critical, status.Unknown, status.Warning, status.OK} {
		if m[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", m[st], st))
		}
	}
	return strings.Join(parts, ", ")
}

// reportText is the report for people, as the email's text.
func reportText(d DailyReportDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Daily report %d of Gaia %s (%s), %s\n\n", d.ID, d.Installation, d.Version, d.GeneratedAt)
	d2 := func(p *float64) string {
		if p == nil {
			return "-"
		}
		return time.Duration(*p * float64(time.Second)).Round(time.Second).String()
	}
	for _, v := range d.VMs {
		fmt.Fprintf(&b, "%-9s %s  since %s\n          proven RPO %s, proven RTO %s, proof age %s, theoretical RPO %s\n          %s\n",
			v.Status, v.VM, orDash(v.Since), d2(v.ProvenRPO), d2(v.ProvenRTO), d2(v.ProofAge), d2(v.TheoreticalRPO), v.Reason)
	}
	if len(d.VMs) == 0 {
		b.WriteString("No VMs are registered.\n")
	}
	fmt.Fprintf(&b, "\nEvidence ledger: %s, %d entries.\n", d.LedgerCheck.Verdict, d.LedgerCheck.Entries)
	if string(d.LedgerHead) != "null" {
		var h struct {
			Payload string `json:"payload"`
		}
		json.Unmarshal(d.LedgerHead, &h)
		fmt.Fprintf(&b, "Ledger head (signed, evidence key %s):\n%s\n", d.EvidenceKeyID, h.Payload)
	}
	if r := d.LastRebuild; r != nil {
		if at, err := time.Parse(time.RFC3339Nano, r.At); err == nil && time.Since(at) < rebuildShown {
			fmt.Fprintf(&b, "REBUILT: this server was rebuilt from its recovery kit (generation %d) at %s, from the state backup %s of %s. "+
				"What the lost server did after that backup was not restored.\n", r.KitGeneration, r.At, r.StateBackup, r.StateBackupCreated)
		}
	}
	fmt.Fprintf(&b, "Recovery kit: %s\n", d.RecoveryKit.Text)
	if len(d.Security.Changes) > 0 {
		fmt.Fprintf(&b, "\nSign-in and security since the previous report:\n")
		for _, c := range d.Security.Changes {
			fmt.Fprintf(&b, "  %s  %s: %s (by %s)\n", c.At, c.Entity, c.Reason, c.Actor)
		}
	}
	for _, u := range d.Security.TokenUses {
		fmt.Fprintf(&b, "API token %s: used %d times on %s\n", u.Name, u.Count, u.Day)
	}
	if d.Notifications.State != status.OK {
		fmt.Fprintf(&b, "Notifications: %s\n", d.Notifications.Text)
	}
	for _, p := range d.Problems {
		fmt.Fprintf(&b, "PROBLEM: %s\n", p)
	}
	fmt.Fprintf(&b, "\nThe attached JSON is this report with the signed ledger head. %s\n", d.HowToVerify)
	return b.String()
}

// notifySummary says whether notifications reach anyone.
func (s *Server) notifySummary(ctx context.Context) (NotifySummary, error) {
	out := NotifySummary{State: status.OK, Failing: []string{}}
	chans, err := s.db.Channels(ctx)
	if err != nil {
		return out, err
	}
	out.Channels = len(chans)
	for _, ch := range chans {
		if !ch.Enabled {
			continue
		}
		out.Enabled++
		if s.cfg.secretMissing(ch) { // it cannot send: say so now, not after its first message fails
			out.Failing = append(out.Failing, fmt.Sprintf("%s: its secret (SMTP password or webhook secret) is not on this server, so it cannot send "+
				"(a server rebuilt from the recovery kit): turn it off (gaia channels set %d enabled=false) and add it again under a new name (install.md step 13)", ch.Name, ch.ID))
			continue
		}
		ns, err := s.db.Notifications(ctx, ch.ID, 1)
		if err != nil {
			return out, err
		}
		if len(ns) == 1 && (ns[0].State == "failed" || ns[0].State == "queued" && ns[0].Attempts > 0) {
			out.Failing = append(out.Failing, fmt.Sprintf("%s: notification %d %s after %d attempt(s): %s", ch.Name, ns[0].ID, ns[0].State, ns[0].Attempts, ns[0].LastError))
		}
	}
	switch {
	case out.Enabled == 0:
		out.State, out.Text = status.Warning, "no notification channel is enabled: nobody is told when a VM's status changes"
	case len(out.Failing) > 0:
		out.State, out.Text = status.Warning, "notifications are failing: "+strings.Join(out.Failing, "; ")
	default:
		out.Text = fmt.Sprintf("%d channel(s) enabled, the last message of each was sent or is waiting", out.Enabled)
	}
	return out, nil
}

// ledgerSummary is the latest ledger check, for the system info.
func (s *Server) ledgerSummary(ctx context.Context) (LedgerSummary, error) {
	cs, err := s.db.LedgerChecks(ctx, 1)
	if err != nil {
		return LedgerSummary{}, err
	}
	if len(cs) == 0 {
		return LedgerSummary{LedgerCheck: store.LedgerCheck{Verdict: evidence.Unknown, Problems: []string{}}, Text: "the evidence ledger was not checked yet"}, nil
	}
	l := LedgerSummary{LedgerCheck: cs[0]}
	head, err := s.db.LedgerHead(ctx)
	if err != nil {
		return LedgerSummary{}, err
	}
	l.Unchecked = max(0, head-cs[0].HeadSeq)
	l.Text = fmt.Sprintf("%s at %s: %d entries", cs[0].Verdict, cs[0].CheckedAt, cs[0].Entries)
	if len(cs[0].Problems) > 0 {
		l.Text += ": " + strings.Join(cs[0].Problems, "; ")
	}
	if l.Unchecked > 0 {
		l.Text += fmt.Sprintf("; %d newer not checked yet", l.Unchecked)
	}
	return l, nil
}

// --- API handlers ---

// LedgerInfo is the latest ledger check and the recent ones.
type LedgerInfo struct {
	Latest LedgerSummary       `json:"latest"`
	Checks []store.LedgerCheck `json:"checks"`
}

func (s *Server) ledgerInfo(c *call) (int, any, error) {
	ctx := c.r.Context()
	l, err := s.ledgerSummary(ctx)
	if err != nil {
		return 0, nil, err
	}
	cs, err := s.db.LedgerChecks(ctx, 20)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, LedgerInfo{Latest: l, Checks: cs}, nil
}

func (s *Server) verifyLedger(c *call) (int, any, error) {
	return one(s.checkLedger(c.r.Context(), c.user.Username, "requested by "+c.user.Username))
}

func (s *Server) createDailyReport(c *call) (int, any, error) {
	r, err := s.makeDailyReport(c.r.Context(), c.user.Username, "requested by "+c.user.Username)
	return created(c, "daily report", r, r.ID, err)
}
