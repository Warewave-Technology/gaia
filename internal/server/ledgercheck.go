package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/status"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Ledger checks (ADR-007 C14): the evidence ledger is checked every hour, with every daily report, and on "Check
// now". An hourly check and "Check now" are incremental: the entries since the last successful check, and the link to
// the last entry it checked. The daily report's check is full (every entry, every bundle), and an hourly check is
// full when no full check ran for a day.
const (
	ledgerCheckEvery     = time.Hour
	ledgerFullEvery      = 24 * time.Hour
	ledgerUncheckedLimit = 2 * time.Hour // unchecked entries are OK this long; then the check is overdue
)

// The ledger summary's codes (ADR-007 C14).
const (
	ledgerOK          = "ok"               // OK: nothing waits
	ledgerWaiting     = "waiting"          // OK: entries wait for the next check, all younger than 2 hours
	ledgerOverdue     = "check_overdue"    // UNKNOWN: no successful check for 2 hours
	ledgerCheckFailed = "check_failed"     // UNKNOWN: the latest check failed to run
	ledgerNever       = "never_checked"    // UNKNOWN
	ledgerBroken      = "ledger_broken"    // CRITICAL: a check found a problem
	ledgerNoEvidence  = "evidence_missing" // CRITICAL: no entries while restore tests have passed
)

// LedgerSummary is the latest ledger check and what it means now: status and code by ADR-007 C14's rule.
type LedgerSummary struct {
	store.LedgerCheck
	Status            string `json:"status"`              // OK, UNKNOWN, CRITICAL
	Code              string `json:"code"`                // ok, waiting, check_overdue, check_failed, never_checked, ledger_broken, evidence_missing
	Unchecked         int    `json:"unchecked"`           // entries sealed after this check: not vouched for until the next one
	OldestUncheckedAt string `json:"oldest_unchecked_at"` // when the oldest of them was sealed; "" when none
	Text              string `json:"text"`
}

// checkLedger verifies the evidence ledger against the installation's key, the head of the latest daily report and
// the catalog (which knows the highest entry a bundle got), and records the result with its kind. An incremental
// check continues the latest check when it was OK; otherwise it is a full one. A check that cannot run is recorded
// too (UNKNOWN), so the summary says it failed instead of showing an older OK.
func (s *Server) checkLedger(ctx context.Context, actor, reason, kind string) (store.LedgerCheck, error) {
	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()
	c, err := s.runLedgerCheck(ctx, reason, kind)
	if err != nil {
		if ctx.Err() != nil {
			return c, err // stopping: not a failed check
		}
		c = store.LedgerCheck{Reason: reason, Kind: c.Kind, Verdict: evidence.Unknown, Problems: []string{"the check could not run: " + err.Error()}}
		if _, rerr := s.db.AddLedgerCheck(context.WithoutCancel(ctx), actor, c); rerr != nil {
			return c, errors.Join(err, rerr)
		}
		return c, err
	}
	return s.db.AddLedgerCheck(context.WithoutCancel(ctx), actor, c)
}

func (s *Server) runLedgerCheck(ctx context.Context, reason, kind string) (store.LedgerCheck, error) {
	c := store.LedgerCheck{Reason: reason, Kind: kind, Problems: []string{}}
	var since store.LedgerCheck
	if kind == store.LedgerCheckIncremental {
		cs, err := s.db.LedgerChecks(ctx, 1)
		if err != nil {
			c.Kind = store.LedgerCheckFull
			return c, err
		}
		if len(cs) == 1 && cs[0].Verdict == evidence.OK && cs[0].HeadSeq > 0 && cs[0].HeadSHA256 != "" {
			since = cs[0]
		} else {
			c.Kind = store.LedgerCheckFull // nothing OK to continue
		}
	}
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
		r, err := evidence.VerifyLedgerSince(s.cfg.ledgerPath(), s.cfg.bundlesDir(), pub, heads, since.HeadSeq, since.HeadSHA256)
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
	return c, nil
}

// ledgerCheckIfDue runs the hourly check: incremental, or full when no full check ran for a day.
func (s *Server) ledgerCheckIfDue(ctx context.Context, now time.Time) error {
	cs, err := s.db.LedgerChecks(ctx, int(ledgerFullEvery/ledgerCheckEvery)*2)
	if err != nil {
		return err
	}
	if len(cs) > 0 {
		at, err := time.Parse(time.RFC3339Nano, cs[0].CheckedAt)
		if err != nil {
			return fmt.Errorf("ledger check %d: checked_at: %w", cs[0].ID, err)
		}
		if now.Sub(at) < ledgerCheckEvery {
			return nil
		}
	}
	kind := store.LedgerCheckFull
	for _, c := range cs {
		if at, err := time.Parse(time.RFC3339Nano, c.CheckedAt); err == nil && c.Kind == store.LedgerCheckFull && now.Sub(at) < ledgerFullEvery {
			kind = store.LedgerCheckIncremental
			break
		}
	}
	_, err = s.checkLedger(ctx, "system", "hourly", kind)
	return err
}

// ledgerSummary is the latest ledger check and the ledger's state by ADR-007 C14's rule, worst first:
//
//	ledger_broken     CRITICAL  the latest check found a problem
//	evidence_missing  CRITICAL  the ledger has no entries while restore tests have passed
//	check_failed      UNKNOWN   the latest check failed to run (or could not decide)
//	never_checked     UNKNOWN   no check ever
//	check_overdue     UNKNOWN   no successful check for 2 hours (or entries wait longer than that)
//	waiting           OK        entries wait for the next check, the oldest younger than 2 hours
//	ok                OK        nothing waits
func (s *Server) ledgerSummary(ctx context.Context) (LedgerSummary, error) {
	now := s.now().UTC()
	cs, err := s.db.LedgerChecks(ctx, 1)
	if err != nil {
		return LedgerSummary{}, err
	}
	head, err := s.db.LedgerHead(ctx)
	if err != nil {
		return LedgerSummary{}, err
	}
	passed, err := s.db.AnyTestPassed(ctx)
	if err != nil {
		return LedgerSummary{}, err
	}
	l := LedgerSummary{LedgerCheck: store.LedgerCheck{Verdict: evidence.Unknown, Problems: []string{}}}
	if len(cs) == 1 {
		l.LedgerCheck = cs[0]
	}
	l.Unchecked = max(0, head-l.HeadSeq)
	var oldest time.Time
	if l.Unchecked > 0 {
		at, err := s.db.OldestBundleAfter(ctx, l.HeadSeq)
		if err != nil {
			return l, err
		}
		if oldest, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return l, fmt.Errorf("evidence bundle after entry %d: created_at %q: %w", l.HeadSeq, at, err)
		}
		l.OldestUncheckedAt = at
	}
	set := func(st, code, text string) { l.Status, l.Code, l.Text = st, code, text }
	var checkedAt time.Time
	if len(cs) == 1 {
		if checkedAt, err = time.Parse(time.RFC3339Nano, l.CheckedAt); err != nil {
			return l, fmt.Errorf("ledger check %d: checked_at: %w", l.ID, err)
		}
	}
	switch {
	case len(cs) == 1 && l.Verdict == evidence.Critical:
		set(status.Critical, ledgerBroken, fmt.Sprintf("the %s check at %s found a problem: %s", l.Kind, l.CheckedAt, strings.Join(l.Problems, "; ")))
	case head == 0 && passed && (len(cs) == 0 || l.Entries <= 1):
		set(status.Critical, ledgerNoEvidence, "restore tests have passed, but the evidence ledger has no entries: tests that leave no evidence are a fault")
	case len(cs) == 1 && l.Verdict != evidence.OK:
		set(status.Unknown, ledgerCheckFailed, fmt.Sprintf("the %s check at %s did not finish: %s", l.Kind, l.CheckedAt, strings.Join(l.Problems, "; ")))
	case len(cs) == 0:
		set(status.Unknown, ledgerNever, "the evidence ledger was not checked yet")
	case now.Sub(checkedAt) >= ledgerUncheckedLimit || l.Unchecked > 0 && now.Sub(oldest) >= ledgerUncheckedLimit:
		set(status.Unknown, ledgerOverdue, fmt.Sprintf("no successful check since %s (a check is due every hour)", l.CheckedAt))
	case l.Unchecked > 0:
		set(status.OK, ledgerWaiting, fmt.Sprintf("OK at %s: %d entries; %d newer wait for the next check (the oldest sealed at %s)", l.CheckedAt, l.Entries, l.Unchecked, l.OldestUncheckedAt))
	default:
		set(status.OK, ledgerOK, fmt.Sprintf("OK at %s: %d entries (%s check)", l.CheckedAt, l.Entries, l.Kind))
	}
	return l, nil
}
