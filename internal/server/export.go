package server

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/docs/spec"
	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// The auditor's evidence export (T20, ADR-007 §7): everything needed to verify the installation's evidence offline,
// without Gaia: every bundle, the ledger, the trust anchor and the current public key, the newest daily report (whose
// signed ledger head shows the ledger was not cut short since), the specification it is all verified against, a
// summary of the restore tests, and how to verify it.
//
// ADR-007 C7 (T28): SHA256SUMS is signed by the installation's evidence key (SHA256SUMS.sig, internal/evidence/sums.go),
// and every export is recorded as an evidence bundle (export-<time>-<random>) whose manifest describes it: who made
// it, its scope and period, and the hash of its SHA256SUMS; with its ledger entry, like any other bundle. The record
// is sealed after the export is complete (it holds the export's SHA256SUMS), so it is in the server's ledger and in
// every later export, not in the export it records. The export's own EXPORT.json says the same (ledger_record).
//
// ADR-007 C18.5: an export the ledger cannot record is given all the same (the auditor may need it exactly then). Its
// README and EXPORT.json say "not recorded in the ledger: <reason>", SHA256SUMS is still signed, it is an audit entry,
// and a state change (evidence_export: not_recorded) sends the security notification export_not_recorded. Its record
// is kept as a pending record (a file under <data>/evidence/pending-exports) and appended, as an ordinary export
// bundle that says it was recorded late, by the next successful ledger write: after a restore test's bundle or another
// export's record is appended, and after a ledger check (hourly, daily, "Check now") that finds the ledger intact.
// Migration checks' appends do not call it (internal/server/migration.go is not this change's); the hourly check
// follows within the hour. ADR-007 C19.3: the pending records are members of the state backup (statebackup.go), and
// restore-state re-queues them (restore.go), so a rebuilt server appends them; C19.6: until then the Evidence screen
// lists each as an export "not recorded in the ledger" (views_evidence.go).

// AuditTest is one restore test in the export's summary.
type AuditTest struct {
	TestID     int64    `json:"test_id"`
	VM         string   `json:"vm"`
	PointID    int64    `json:"point_id"`
	FinishedAt string   `json:"finished_at"`
	Verdict    string   `json:"verdict"`
	ProvenRTOS *float64 `json:"proven_rto_s"`
	RTOTargetS *int64   `json:"rto_target_s"`
	Reason     string   `json:"reason"`
	BundleID   string   `json:"bundle_id"`
	LedgerSeq  int      `json:"ledger_seq"`
}

// exportFormat names the export bundle's manifest member "export" (an extension member: v1 verifiers ignore it, §2),
// and is the format of the export's own EXPORT.json.
const exportFormat = evidence.ExportManifestFormat

// ExportManifest describes an evidence export: in the export itself (EXPORT.json, without the three SHA256SUMS
// fields, which cannot be in the files SHA256SUMS lists) and in its record's manifest (member "export", with them).
type ExportManifest struct {
	Format        string       `json:"format"` // gaia-evidence-export
	FormatVersion int          `json:"format_version"`
	Name          string       `json:"name"` // the export's directory (and file) name
	Made          string       `json:"made"`
	Installation  string       `json:"installation"`
	ServerVersion string       `json:"server_version"`
	By            ExportBy     `json:"by"`
	Scope         ExportScope  `json:"scope"`
	Period        ExportPeriod `json:"period"`
	Files         int          `json:"files,omitempty"`             // files SHA256SUMS lists (the record only)
	SumsSHA256    string       `json:"sha256sums_sha256,omitempty"` // the hash of the export's SHA256SUMS (the record only)
	SumsKeyID     string       `json:"sha256sums_key_id,omitempty"` // the key that signed it, SHA256SUMS.sig (the record only)
	// LedgerRecord: the export's record as the export states it when it is given (ADR-007 C18.5, C18.6).
	LedgerRecord ExportLedgerRecord `json:"ledger_record"`
}

// ExportLedgerRecord is an export's record in the ledger, as the export states it.
type ExportLedgerRecord struct {
	// State: sealed_after_export (recorded as BundleID once the export is complete), not_recorded (the ledger could not
	// record it: Reason; BundleID is appended with the next successful ledger write, "" when even that record could not
	// be kept), audit_only (a single-bundle export: an audit entry, no ledger record).
	State    string `json:"state"`
	BundleID string `json:"bundle_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ExportBy is who made an export.
type ExportBy struct {
	User   string `json:"user"`
	UserID int64  `json:"user_id"`
	Role   string `json:"role"`
	Via    string `json:"via"` // session or api_token
}

// ExportScope is what an export holds.
type ExportScope struct {
	Kind             string `json:"kind"`             // all: the whole ledger and every bundle; bundle: the whole ledger and one bundle
	Bundle           string `json:"bundle,omitempty"` // kind bundle: the bundle exported
	LedgerEntries    int    `json:"ledger_entries"`
	LedgerHeadSHA256 string `json:"ledger_head_sha256"` // the entry hash of the last entry exported
	Bundles          int    `json:"bundles"`
	DailyReport      *int64 `json:"daily_report"` // the newest daily report included, if any
}

// ExportPeriod is the time the exported ledger covers: the times of its first and last entry.
type ExportPeriod struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// The state changes of an export that the ledger could not record (entity evidence_export; its id is the export's
// time in Unix milliseconds): "" -> not_recorded when it is given (the security notification export_not_recorded),
// not_recorded -> recorded when its record is appended.
const (
	exportEntity      = "evidence_export"
	exportNotRecorded = "not_recorded"
	exportRecorded    = "recorded"
)

// evExportNotRec is the notification event of an export given without its ledger record (category security).
const evExportNotRec = "export_not_recorded"

const exportNotRecordedSubject = "an evidence export was given without its record in the evidence ledger"

func exportNotRecordedText(sc store.StateChange) string {
	return fmt.Sprintf("At %s: %s.\nThe export is complete and its SHA256SUMS is signed; only its record in the evidence ledger is missing, "+
		"and its README says so. A ledger that refuses entries is a ledger problem: the Evidence screen's ledger check says what is wrong. "+
		"The audit log has the export.", sc.At, sc.Reason)
}

// exportFiles are an export's files in the order they are written, with their hashes for SHA256SUMS.
type exportFiles struct {
	files []exportFile
	sums  map[string]string
}

type exportFile struct {
	name string
	body []byte
}

func newExportFiles() *exportFiles { return &exportFiles{sums: map[string]string{}} }

func (x *exportFiles) add(name string, body []byte) {
	x.files = append(x.files, exportFile{name, body})
	x.sums[name] = sha256Hex(body)
}

func (x *exportFiles) clone() *exportFiles {
	return &exportFiles{files: slices.Clone(x.files), sums: maps.Clone(x.sums)}
}

// seal adds SHA256SUMS of the files so far and its signature, and returns both and how many files SHA256SUMS lists.
func (x *exportFiles) seal(key ed25519.PrivateKey, now time.Time) (sums, sig []byte, listed int, err error) {
	sums = evidence.SumsLines(x.sums)
	if sig, err = evidence.SignSums(sums, key, now); err != nil {
		return nil, nil, 0, err
	}
	listed = len(x.sums)
	x.add(evidence.SumsFile, sums)
	x.add(evidence.SumsSignatureFile, sig)
	return sums, sig, listed, nil
}

// tarGz lays the files out under root in a gzipped tar.
func (x *exportFiles) tarGz(root string, mod time.Time) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range x.files {
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + f.name, Mode: 0o644, Size: int64(len(f.body)), ModTime: mod}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// exportManifestFile is EXPORT.json: the manifest without the SHA256SUMS fields.
func exportManifestFile(m ExportManifest) ([]byte, error) {
	m.Files, m.SumsSHA256, m.SumsKeyID = 0, "", ""
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// newExportBundleID names an export's record: export-<time>-<random>.
func newExportBundleID(now time.Time) (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return "export-" + now.Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix), nil
}

func (s *Server) auditExport(c *call) (int, any, error) {
	ctx := c.r.Context()
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	now := s.now().UTC()
	c.target = "evidence:export"
	if _, err := os.Stat(s.cfg.ledgerPath()); errors.Is(err, os.ErrNotExist) {
		return 0, nil, fmt.Errorf("%w: no evidence was sealed yet (the first restore test seals the first bundle): nothing to export", store.ErrConflict)
	}
	anchor, err := evidence.GenesisKey(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, fmt.Errorf("the ledger's trust anchor: %w", err)
	}
	ledger, err := os.ReadFile(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, err
	}
	span, err := spanOf(ledger)
	if err != nil {
		return 0, nil, fmt.Errorf("the ledger: %w", err)
	}
	current, err := currentKey(s.cfg)
	if err != nil {
		return 0, nil, err
	}
	key, err := evidence.LoadPrivateKey(s.cfg.keyPath())
	if err != nil {
		return 0, nil, err
	}
	root := "gaia-evidence-" + now.Format("20060102T150405Z")
	bundleID, err := newExportBundleID(now)
	if err != nil {
		return 0, nil, err
	}
	base := newExportFiles()
	base.add("anchor.pub", []byte(base64.StdEncoding.EncodeToString(anchor)+"\n"))
	base.add("keys/evidence-key-current.pub", current)
	base.add(evidence.ExportLedgerFile, ledger)
	bundles := 0
	err = filepath.WalkDir(s.cfg.bundlesDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(s.cfg.bundlesDir(), p)
		if err != nil {
			return err
		}
		if filepath.Base(rel) == evidence.ManifestFile && filepath.Dir(rel) != "." && filepath.Dir(filepath.Dir(rel)) == "." {
			bundles++
		}
		base.add(evidence.ExportBundlesDir+"/"+filepath.ToSlash(rel), b)
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	report := "no daily report was made yet: the export cannot show that the ledger was not cut short"
	verifyHead := ""
	var reportID *int64
	if rs, err := s.db.DailyReports(ctx, 1); err != nil {
		return 0, nil, err
	} else if len(rs) == 1 {
		reportID = &rs[0].ID
		name := fmt.Sprintf("daily-report-%d.json", rs[0].ID)
		base.add(name, rs[0].Report)
		report = fmt.Sprintf("%-26s the newest daily report (%s)", name, rs[0].GeneratedAt)
		if string(rs[0].LedgerHead) != "null" {
			head := fmt.Sprintf("ledger-head-%d.json", rs[0].ID)
			base.add(head, append(append([]byte{}, rs[0].LedgerHead...), '\n'))
			report += fmt.Sprintf("\n  %-26s its signed ledger head (SPEC.md §6.8): entry %d", head, rs[0].HeadSeq)
			verifyHead = head
		}
	}
	tests, err := s.auditTests(c)
	if err != nil {
		return 0, nil, err
	}
	tb, err := json.MarshalIndent(tests, "", "  ")
	if err != nil {
		return 0, nil, err
	}
	base.add("restore-tests.json", append(tb, '\n'))
	base.add("SPEC.md", spec.EvidenceV1)
	signer := evidence.KeyID(key.Public().(ed25519.PublicKey))
	via := "session"
	if c.token != nil {
		via = "api_token"
	}
	m := ExportManifest{Format: exportFormat, FormatVersion: 1, Name: root, Made: now.Format(time.RFC3339), Installation: hostname(),
		ServerVersion: s.version, By: ExportBy{User: c.user.Username, UserID: c.user.ID, Role: c.user.Role, Via: via},
		Scope:  ExportScope{Kind: evidence.ExportScopeAll, LedgerEntries: span.Entries, LedgerHeadSHA256: span.HeadSHA256, Bundles: bundles, DailyReport: reportID},
		Period: ExportPeriod{From: span.From, To: span.To}}
	rd := exportReadmeData{made: now, version: s.version, anchorID: evidence.KeyID(anchor), signerID: signer, ledgerBytes: len(ledger),
		entries: span.Entries, bundles: bundles, report: report, head: verifyHead}
	// finish adds what depends on the record's state: the README, EXPORT.json, SHA256SUMS and its signature. The
	// record is tried with the export as it would be handed out; when the ledger refuses it, the export is finished
	// again, saying so, and that one is given (and its SHA256SUMS is what the pending record holds).
	finish := func(rec ExportLedgerRecord) (*exportFiles, ExportManifest, []byte, []byte, error) {
		x, fm := base.clone(), m
		fm.LedgerRecord = rec
		rd.record = rec
		x.add("README.txt", []byte(exportReadme(rd)))
		ej, err := exportManifestFile(fm)
		if err != nil {
			return nil, fm, nil, nil, err
		}
		x.add(evidence.ExportManifestFile, ej)
		sums, sig, listed, err := x.seal(key, now)
		if err != nil {
			return nil, fm, nil, nil, err
		}
		fm.Files, fm.SumsSHA256, fm.SumsKeyID = listed, sha256Hex(sums), signer
		return x, fm, sums, sig, nil
	}
	x, fm, sums, sig, err := finish(ExportLedgerRecord{State: evidence.ExportRecordSealedAfter, BundleID: bundleID})
	if err != nil {
		return 0, nil, err
	}
	h := c.w.Header()
	rec, recErr := s.recordExport(ctx, bundleID, fm, sums, sig, key, now)
	if recErr == nil {
		c.detail = fmt.Sprintf("full evidence export: %d bundles, ledger %d entries (%d bytes); SHA256SUMS %s signed by key %s; recorded as bundle %s, ledger entry %d",
			bundles, span.Entries, len(ledger), fm.SumsSHA256, signer, rec.BundleID, rec.LedgerSeq)
		h.Set("X-Gaia-Export-Bundle", rec.BundleID)
		// The ledger takes entries again: the records of earlier exports it could not take follow this one.
		if err := s.recordPendingExports(context.WithoutCancel(ctx)); err != nil {
			s.log.Error("appending the records of exports the ledger could not record", "error", err)
		}
	} else {
		// ADR-007 C18.5: given all the same. The record is kept as pending under a new bundle id (the refused attempt's
		// id is never reused: a refused append may have left its directory), and appended by the next ledger write.
		reason := reasonText(recErr)
		pendingID, err := newExportBundleID(now)
		if err != nil {
			return 0, nil, err
		}
		if x, fm, sums, sig, err = finish(ExportLedgerRecord{State: evidence.ExportRecordNotRecorded, BundleID: pendingID, Reason: reason}); err != nil {
			return 0, nil, err
		}
		p := pendingExport{BundleID: pendingID, Manifest: fm, Sums: string(sums), SumsSig: string(sig), StateID: now.UnixMilli()}
		kept := fmt.Sprintf("its record is kept and appended as bundle %s with the next successful ledger write", pendingID)
		if kerr := s.keepPendingExport(p); kerr != nil {
			s.log.Error("keeping the record of an export the ledger could not record", "bundle", pendingID, "error", kerr)
			reason += "; the server could not keep the record to append it later either (" + reasonText(kerr) + "): the audit log entry is its only record"
			if x, fm, _, _, err = finish(ExportLedgerRecord{State: evidence.ExportRecordNotRecorded, Reason: reason}); err != nil {
				return 0, nil, err
			}
			kept, pendingID = "its record could not be kept either: "+reasonText(kerr), ""
		}
		text := fmt.Sprintf("evidence export %s by %s (%s, via %s) was given without its record in the evidence ledger: %s; %s",
			root, m.By.User, m.By.Role, via, reasonText(recErr), kept)
		c.detail = fmt.Sprintf("full evidence export: %d bundles, ledger %d entries (%d bytes); SHA256SUMS %s signed by key %s; NOT recorded in the ledger: %s; %s",
			bundles, span.Entries, len(ledger), fm.SumsSHA256, signer, reasonText(recErr), kept)
		if err := s.db.RecordState(context.WithoutCancel(ctx), exportEntity, p.StateID, "", exportNotRecorded, text, c.user.Username); err != nil {
			// The export is still given: the audit entry has it, and the log says why no notification follows.
			s.log.Error("recording an export the ledger could not record (no notification follows)", "export", root, "error", err)
			c.detail += "; recording the state change failed (no notification): " + reasonText(err)
		}
		h.Set("X-Gaia-Export-Not-Recorded", oneLine(recErr.Error(), 400))
		if pendingID != "" {
			h.Set("X-Gaia-Export-Pending-Record", pendingID)
		}
	}
	out, err := x.tarGz(root, now)
	if err != nil {
		return 0, nil, err
	}
	c.raw = true
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", root+".tar.gz"))
	c.w.WriteHeader(http.StatusOK)
	_, err = c.w.Write(out)
	return http.StatusOK, nil, err
}

// exportEvidence is the single-bundle export (ADR-007 C18.6): one bundle with the whole ledger and the trust anchor,
// EXPORT.json (scope bundle, ledger_record audit_only), and SHA256SUMS signed like the full export's. It is an audit
// entry (the route's auditRead), not a ledger bundle. gaia evidence verify -export checks it: the files against
// SHA256SUMS, the signature, every ledger entry, and the one bundle; the bundles it leaves out are counted, not missing.
func (s *Server) exportEvidence(c *call) (int, any, error) {
	ctx := c.r.Context()
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	e, err := s.db.Evidence(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	c.target = "evidence:" + e.BundleID
	now := s.now().UTC()
	anchor, err := evidence.GenesisKey(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, err
	}
	ledger, err := os.ReadFile(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, err
	}
	span, err := spanOf(ledger)
	if err != nil {
		return 0, nil, fmt.Errorf("the ledger: %w", err)
	}
	key, err := evidence.LoadPrivateKey(s.cfg.keyPath())
	if err != nil {
		return 0, nil, err
	}
	signer := evidence.KeyID(key.Public().(ed25519.PublicKey))
	root := "gaia-evidence-" + e.BundleID
	x := newExportFiles()
	x.add("anchor.pub", []byte(base64.StdEncoding.EncodeToString(anchor)+"\n"))
	x.add(evidence.ExportLedgerFile, ledger)
	dir := filepath.Join(s.cfg.bundlesDir(), e.BundleID)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(s.cfg.bundlesDir(), p)
		if err != nil {
			return err
		}
		x.add(evidence.ExportBundlesDir+"/"+filepath.ToSlash(rel), b)
		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("bundle %s: %w", e.BundleID, err)
	}
	via := "session"
	if c.token != nil {
		via = "api_token"
	}
	m := ExportManifest{Format: exportFormat, FormatVersion: 1, Name: root, Made: now.Format(time.RFC3339), Installation: hostname(),
		ServerVersion: s.version, By: ExportBy{User: c.user.Username, UserID: c.user.ID, Role: c.user.Role, Via: via},
		Scope: ExportScope{Kind: evidence.ExportScopeBundle, Bundle: e.BundleID, LedgerEntries: span.Entries, LedgerHeadSHA256: span.HeadSHA256,
			Bundles: 1},
		Period: ExportPeriod{From: span.From, To: span.To}, LedgerRecord: ExportLedgerRecord{State: evidence.ExportRecordAuditOnly}}
	x.add("README.txt", []byte(fmt.Sprintf(`Evidence bundle %s of Gaia installation %s, exported %s by %s (server version %s).

Contents
  anchor.pub                 the ledger's first key: the trust anchor, key_id %s
  ledger.jsonl               the evidence ledger, %d entries: every entry, not only this bundle's
  bundles/%s/
                             the bundle (ledger entry %d)
  EXPORT.json                this export: who made it, its scope (one bundle) and period
  SHA256SUMS                 the hash of every file above
  SHA256SUMS.sig             the signature of SHA256SUMS by the installation's evidence key, key_id %s

Verify, without Gaia's server
  1. FIRST compare both key_ids above with the one you recorded when Gaia was installed, or with the
     evidence_key_id of a daily report that reached you by email. A verifier must not take the trust anchor from
     the data it verifies; the signature of SHA256SUMS only shows that the files did not change on the way.
  2. In this directory:
       gaia evidence verify -pubkey anchor.pub -export .
     It checks every file against SHA256SUMS, the signature against the key the ledger leads to from the anchor,
     every ledger entry, and this bundle. The other bundles the ledger lists are left out of this export by design:
     the verification counts them (left_out) and does not report them missing.
  3. With any other implementation of the Restore Evidence Format v1 (docs/spec/evidence-v1.md), or
       gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles
     the other bundles show up as bundle_missing: only this bundle's result and the ledger's entries count there.

This export is in the server's audit log; it is not recorded in the evidence ledger (only full exports are).
`, e.BundleID, hostname(), now.Format(time.RFC3339), c.user.Username, s.version, evidence.KeyID(anchor), span.Entries, e.BundleID, e.LedgerSeq, signer)))
	ej, err := exportManifestFile(m)
	if err != nil {
		return 0, nil, err
	}
	x.add(evidence.ExportManifestFile, ej)
	sums, _, _, err := x.seal(key, now)
	if err != nil {
		return 0, nil, err
	}
	out, err := x.tarGz(root, now)
	if err != nil {
		return 0, nil, err
	}
	c.raw = true
	c.detail = fmt.Sprintf("exported bundle %s with the ledger (%d entries), %d bytes; SHA256SUMS %s signed by key %s; an audit entry, no ledger record",
		e.BundleID, span.Entries, len(out), sha256Hex(sums), signer)
	h := c.w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="`+root+`.tar.gz"`)
	c.w.WriteHeader(http.StatusOK)
	if _, err := c.w.Write(out); err != nil {
		s.log.Error("sending an evidence export", "bundle", e.BundleID, "error", err)
	}
	return http.StatusOK, nil, nil
}

// recordExport records a new export: its record is sealed and appended (sealExportRecord). A ledger that refuses the
// entry is a ledger problem, not only this export's: the ledger is checked at once (a change notifies).
func (s *Server) recordExport(ctx context.Context, bundleID string, m ExportManifest, sums, sig []byte, key ed25519.PrivateKey, now time.Time) (store.EvidenceBundle, error) {
	rec, err := s.sealExportRecord(ctx, bundleID, m, sums, sig, key, now, nil)
	if err != nil {
		if _, cerr := s.checkLedger(context.WithoutCancel(ctx), "system", "appending bundle "+bundleID+" failed: "+err.Error(), store.LedgerCheckFull); cerr != nil {
			s.log.Error("ledger check", "error", cerr)
		}
		return rec, fmt.Errorf("the evidence ledger refused the entry: %w", err)
	}
	return rec, nil
}

// sealExportRecord seals an export's record, appends it to the ledger and records it in the catalog. The bundle holds
// the export's SHA256SUMS and its signature; the manifest's subject (strings: for people, and what a v1 verifier
// reads, plus late's) and its member "export" (ExportManifest) describe the export.
func (s *Server) sealExportRecord(ctx context.Context, bundleID string, m ExportManifest, sums, sig []byte, key ed25519.PrivateKey, now time.Time, late map[string]string) (store.EvidenceBundle, error) {
	dir := filepath.Join(s.cfg.bundlesDir(), bundleID)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return store.EvidenceBundle{}, err
	}
	sealed := false
	defer func() {
		if !sealed { // no ledger entry refers to it: leave no directory behind (it would be bundle_unlisted)
			if err := os.RemoveAll(dir); err != nil {
				s.log.Error("removing an unsealed export record", "dir", dir, "error", err)
			}
		}
	}()
	if err := os.Mkdir(filepath.Join(dir, "export"), 0o755); err != nil {
		return store.EvidenceBundle{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "export", evidence.SumsFile), sums, 0o644); err != nil {
		return store.EvidenceBundle{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "export", evidence.SumsSignatureFile), sig, 0o644); err != nil {
		return store.EvidenceBundle{}, err
	}
	subject := map[string]string{"kind": evidence.ExportBundleKind, "export": m.Name, "user": m.By.User, "role": m.By.Role, "via": m.By.Via,
		"scope": m.Scope.Kind, "period_from": m.Period.From, "period_to": m.Period.To, "ledger_entries": fmt.Sprint(m.Scope.LedgerEntries),
		"sha256sums_sha256": m.SumsSHA256, "verdict": evidence.OK}
	maps.Copy(subject, late)
	what := "evidence export by " + m.By.User
	if late != nil {
		what += " (recorded late)"
	}
	sum, err := evidence.SealExt(dir, bundleID, subject, map[string]any{"export": m}, key, now)
	if err != nil {
		return store.EvidenceBundle{}, fmt.Errorf("sealing %s: %w", bundleID, err)
	}
	e, err := evidence.Append(s.cfg.ledgerPath(), evidence.Entry{BundleID: bundleID, BundleSHA256: sum, Subject: what, Verdict: evidence.OK, Time: now}, key)
	if err != nil {
		// The entry may have been written after all (the flush failed): then the record is in the ledger and is kept.
		// When the ledger cannot even be read, whether it was is unknown: the directory is kept too (an entry without
		// its bundle would be bundle_missing; a bundle without its entry is bundle_unlisted, which the check reports).
		seq, serr := evidence.BundleEntry(s.cfg.ledgerPath(), bundleID)
		if serr != nil {
			sealed = true
			s.log.Error("reading the ledger after a failed append", "bundle", bundleID, "error", serr)
			return store.EvidenceBundle{}, err
		}
		if seq == 0 {
			return store.EvidenceBundle{}, err
		}
		s.log.Error("the ledger reported an error but holds the entry: kept", "bundle", bundleID, "entry", seq, "error", err)
		e.Seq = seq
	}
	sealed = true
	return s.db.AddEvidence(context.WithoutCancel(ctx), store.EvidenceBundle{BundleID: bundleID, LedgerSeq: e.Seq, BundleSHA256: sum, Verdict: evidence.OK})
}

// --- the records of exports the ledger could not record (ADR-007 C18.5) ---

// pendingExport is the record of an export that was given while the ledger could not record it: what the export
// bundle will be (who, scope, period, the SUMS hash, in Manifest; the SHA256SUMS and its signature), kept as a file
// until the next successful ledger write appends it.
type pendingExport struct {
	BundleID    string         `json:"bundle_id"`
	Manifest    ExportManifest `json:"manifest"` // as in the record; ledger_record says why it was not recorded
	Sums        string         `json:"sha256sums"`
	SumsSig     string         `json:"sha256sums_sig"`
	StateID     int64          `json:"state_id"` // the evidence_export state changes' id
	Attempts    int            `json:"attempts"` // appends tried since, and the last one's error
	LastAttempt string         `json:"last_attempt,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
}

func (c Config) pendingExportsDir() string {
	return filepath.Join(c.DataDir, "evidence", "pending-exports")
}

// keepPendingExport writes a pending record durably: a new file, synced, renamed into place, the directory synced.
func (s *Server) keepPendingExport(p pendingExport) error {
	dir := s.cfg.pendingExportsDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
				s.log.Error("removing a pending export record's temporary file", "file", tmp, "error", err)
			}
		}
	}()
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, p.BundleID+".json")); err != nil {
		return err
	}
	done = true
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// pendingExports lists the pending records, oldest first (by the export's time in milliseconds, StateID).
func (s *Server) pendingExports() ([]pendingExport, error) {
	des, err := os.ReadDir(s.cfg.pendingExportsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []pendingExport
	for _, d := range des {
		name := d.Name()
		if !d.Type().IsRegular() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.cfg.pendingExportsDir(), name))
		if err != nil {
			return nil, err
		}
		var p pendingExport
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("pending export record %s: %w", name, err)
		}
		if p.BundleID+".json" != name {
			return nil, fmt.Errorf("pending export record %s names bundle %s", name, p.BundleID)
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b pendingExport) int {
		if a.StateID != b.StateID {
			return cmp.Compare(a.StateID, b.StateID)
		}
		return strings.Compare(a.BundleID, b.BundleID)
	})
	return out, nil
}

// recordPendingExports appends the pending records to the ledger, oldest first, after a successful ledger write. The
// caller holds exportMu. The first that the ledger refuses stops it (the next ones would be refused too); its attempt
// is noted in its file and the error returned.
func (s *Server) recordPendingExports(ctx context.Context) error {
	ps, err := s.pendingExports()
	if err != nil || len(ps) == 0 {
		return err
	}
	for _, p := range ps {
		if err := s.appendPendingExport(ctx, p); err != nil {
			p.Attempts++
			p.LastAttempt, p.LastError = s.now().UTC().Format(time.RFC3339), err.Error()
			if werr := s.keepPendingExport(p); werr != nil {
				return errors.Join(fmt.Errorf("the record %s of export %s: %w", p.BundleID, p.Manifest.Name, err), werr)
			}
			return fmt.Errorf("the record %s of export %s: %w", p.BundleID, p.Manifest.Name, err)
		}
	}
	return nil
}

// afterLedgerWrite appends the pending export records after a ledger write that succeeded elsewhere (a restore
// test's bundle, a ledger check that found the ledger intact). An export in progress holds exportMu and appends them
// itself after its own record, so this does not wait for it.
func (s *Server) afterLedgerWrite(ctx context.Context) {
	if ps, err := s.pendingExports(); err != nil {
		s.log.Error("reading the pending export records", "error", err)
		return
	} else if len(ps) == 0 {
		return
	}
	if !s.exportMu.TryLock() {
		return
	}
	defer s.exportMu.Unlock()
	if err := s.recordPendingExports(context.WithoutCancel(ctx)); err != nil {
		s.log.Error("appending the records of exports the ledger could not record", "error", err)
	}
}

// appendPendingExport seals and appends one pending record, records the state change, and removes its file. A record
// whose entry is in the ledger already (an attempt that stopped after its append) is not appended again.
func (s *Server) appendPendingExport(ctx context.Context, p pendingExport) error {
	now := s.now().UTC()
	seq, err := evidence.BundleEntry(s.cfg.ledgerPath(), p.BundleID)
	if err != nil {
		return err
	}
	if seq == 0 {
		// No entry names it: a directory of an attempt cut short is no bundle (it would be bundle_unlisted).
		if err := os.RemoveAll(filepath.Join(s.cfg.bundlesDir(), p.BundleID)); err != nil {
			return err
		}
		key, err := evidence.LoadPrivateKey(s.cfg.keyPath())
		if err != nil {
			return err
		}
		late := map[string]string{"recorded_late": "true", "not_recorded_reason": p.Manifest.LedgerRecord.Reason, "given": p.Manifest.Made}
		rec, err := s.sealExportRecord(ctx, p.BundleID, p.Manifest, []byte(p.Sums), []byte(p.SumsSig), key, now, late)
		if err != nil {
			return err
		}
		seq = rec.LedgerSeq
	} else {
		mb, err := os.ReadFile(filepath.Join(s.cfg.bundlesDir(), p.BundleID, evidence.ManifestFile))
		if err != nil {
			return fmt.Errorf("ledger entry %d names %s: %w", seq, p.BundleID, err)
		}
		if _, err := s.db.AddEvidence(ctx, store.EvidenceBundle{BundleID: p.BundleID, LedgerSeq: seq, BundleSHA256: sha256Hex(mb),
			Verdict: evidence.OK}); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	text := fmt.Sprintf("the record of evidence export %s (by %s, given %s while the ledger could not record it: %s) is appended as bundle %s, ledger entry %d",
		p.Manifest.Name, p.Manifest.By.User, p.Manifest.Made, p.Manifest.LedgerRecord.Reason, p.BundleID, seq)
	if err := s.db.RecordState(ctx, exportEntity, p.StateID, exportNotRecorded, exportRecorded, text, "system"); err != nil {
		return err
	}
	return os.Remove(filepath.Join(s.cfg.pendingExportsDir(), p.BundleID+".json"))
}

// reasonText is an error as one line (README, EXPORT.json, the audit entry, the notification): oneLine, long enough
// for a ledger's findings.
func reasonText(err error) string { return oneLine(err.Error(), 4000) }

// ledgerSpan is what a ledger covers: its entries, the times of the first and the last, and the last one's hash.
type ledgerSpan struct {
	Entries    int
	From, To   string
	HeadSHA256 string
}

func spanOf(ledger []byte) (ledgerSpan, error) {
	var sp ledgerSpan
	if len(ledger) == 0 {
		return sp, nil
	}
	for i, l := range bytes.Split(bytes.TrimSuffix(ledger, []byte("\n")), []byte("\n")) {
		var w struct {
			Payload string `json:"payload"`
		}
		var p struct {
			Time string `json:"time"`
		}
		if err := json.Unmarshal(l, &w); err != nil {
			return sp, fmt.Errorf("line %d: %w", i+1, err)
		}
		if err := json.Unmarshal([]byte(w.Payload), &p); err != nil {
			return sp, fmt.Errorf("line %d: payload: %w", i+1, err)
		}
		if i == 0 {
			sp.From = p.Time
		}
		sp.Entries, sp.To, sp.HeadSHA256 = i+1, p.Time, sha256Hex([]byte(w.Payload))
	}
	return sp, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type exportReadmeData struct {
	made               time.Time
	version            string
	anchorID, signerID string
	ledgerBytes        int
	entries, bundles   int
	report, head       string
	record             ExportLedgerRecord
}

// exportReadme is the export's README.txt. Its first verification step stays the comparison of the key_ids with a
// daily report received outside the export (ADR-007 C7: the signature is integrity in transit, not a trust anchor).
func exportReadme(d exportReadmeData) string {
	return fmt.Sprintf(`Evidence export of Gaia installation %s, %s (server version %s).
%s
Contents
  anchor.pub                 the ledger's first key: the trust anchor, key_id %s
  keys/                      the installation's current public key
  ledger.jsonl               the evidence ledger, %d entries, %d bytes
  bundles/                   %d evidence bundles (restore tests, migration checks, records of earlier exports)
  %s
  restore-tests.json         every restore test: VM, point, time, verdict, measured RTO against its target, bundle
  SPEC.md                    Restore Evidence Format, version 1 (CC0): what everything here means and how to verify it
  EXPORT.json                this export: who made it, its scope and period, and its record in the ledger
  SHA256SUMS                 the hash of every file above
  SHA256SUMS.sig             the signature of SHA256SUMS by the installation's evidence key, key_id %s

Verify, without Gaia's server
  1. FIRST compare both key_ids above (the trust anchor's, and the one that signed SHA256SUMS) with the
     evidence_key_id of a daily report that reached you outside this export (by email), or with the key_id you
     recorded when Gaia was installed. If they differ, stop: nothing here can be trusted. The signature of SHA256SUMS
     only shows that the files did not change on the way; it is not a trust anchor, and a verifier must not take the
     anchor from the data it verifies (SPEC.md §3).
  2. With Gaia's command-line tool (any machine, no server), in this directory:
       gaia evidence verify -pubkey anchor.pub -export .
     It checks every file against SHA256SUMS, the signature of SHA256SUMS against the key the ledger leads to from
     the anchor, and the ledger with every bundle%s. The ledger alone:
       gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles%s
  3. With any other implementation of SPEC.md, for example the independent Python verifier in Gaia's source
     (docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py, standard library only):
       python3 -I verify.py ledger ledger.jsonl bundles --key anchor.pub%s
     and with standard tools: sha256sum -c SHA256SUMS. SHA256SUMS.sig is JSON: public_key is the base64 of the
     32-byte Ed25519 key (its SHA-256 is key_id), signature the base64 of its Ed25519 signature of SHA256SUMS's
     bytes as they are.
  OK means: every entry from the anchor on is signed and chained, every bundle is exactly as sealed, no bundle is
  missing or extra%s.
  A copy of the daily report that reached you by email is a stronger head than the one in this export: give that one
  with -head (--head) as well.

%s`, hostname(), d.made.Format(time.RFC3339), d.version, readmeNotRecorded(d), d.anchorID, d.entries, d.ledgerBytes, d.bundles, d.report, d.signerID,
		opt(d.head != "", " and the daily report's head ("+d.head+")"), opt(d.head != "", " -head "+d.head), opt(d.head != "", " --head "+d.head),
		opt(d.head != "", ",\n  and the ledger still holds the head the daily report recorded"), readmeRecord(d))
}

// readmeNotRecorded is the README's warning at the top for an export the ledger could not record.
func readmeNotRecorded(d exportReadmeData) string {
	if d.record.State != evidence.ExportRecordNotRecorded {
		return ""
	}
	return "\nNOT RECORDED IN THE LEDGER: " + d.record.Reason + "\n"
}

// readmeRecord is the README's last section: the export's record.
func readmeRecord(d exportReadmeData) string {
	switch {
	case d.record.State == evidence.ExportRecordSealedAfter:
		return fmt.Sprintf(`This export is recorded
  Gaia recorded this export as evidence bundle %s: its manifest names who made it, its scope and period, and
  the SHA-256 of this SHA256SUMS; its ledger entry follows entry %d. The record is made once the export is complete,
  so it is not in this export: it is in the server's ledger and in every later export, where
  gaia evidence verify -export lists the exports the ledger records.
`, d.record.BundleID, d.entries)
	case d.record.BundleID != "":
		return fmt.Sprintf(`This export is not recorded in the ledger
  not recorded in the ledger: %s
  The export is complete and SHA256SUMS is signed as always; only its record in the evidence ledger is missing.
  Gaia keeps the record (who made it, its scope and period, the SHA-256 of this SHA256SUMS) and appends it as
  evidence bundle %s with its next successful ledger write; every export after that lists it. The export is in
  the audit log, and the server sent a security notification (export_not_recorded). gaia evidence verify -export
  reports export_not_recorded (UNKNOWN) for this export: whether it was recorded later is shown by a later export.
`, d.record.Reason, d.record.BundleID)
	default:
		return fmt.Sprintf(`This export is not recorded in the ledger
  not recorded in the ledger: %s
  The export is complete and SHA256SUMS is signed as always; only its record in the evidence ledger is missing, and
  will not be appended later. The export is in the audit log, and the server sent a security notification
  (export_not_recorded). gaia evidence verify -export reports export_not_recorded (UNKNOWN) for this export.
`, d.record.Reason)
	}
}

// auditTests lists every restore test with its VM, target and evidence, oldest first.
func (s *Server) auditTests(c *call) ([]AuditTest, error) {
	ctx := c.r.Context()
	vms, err := s.db.VMs(ctx, 0)
	if err != nil {
		return nil, err
	}
	names, targets := map[int64]string{}, map[int64]*int64{}
	for _, vm := range vms {
		names[vm.ID] = vm.Name
		if vm.PolicyID != nil {
			p, err := s.db.Policy(ctx, *vm.PolicyID)
			if err != nil {
				return nil, err
			}
			t := p.RTOTargetS
			targets[vm.ID] = &t
		}
	}
	ev, err := s.db.EvidenceBundles(ctx, 0, 1000000)
	if err != nil {
		return nil, err
	}
	seq := map[string]int{}
	for _, e := range ev {
		seq[e.BundleID] = e.LedgerSeq
	}
	tests, err := s.db.RestoreTests(ctx, 0, 1000000)
	if err != nil {
		return nil, err
	}
	out := make([]AuditTest, 0, len(tests))
	for i := len(tests) - 1; i >= 0; i-- {
		t := tests[i]
		out = append(out, AuditTest{TestID: t.ID, VM: names[t.VMID], PointID: t.PointID, FinishedAt: t.FinishedAt, Verdict: t.Verdict,
			ProvenRTOS: t.ProvenRTOS, RTOTargetS: targets[t.VMID], Reason: t.Reason, BundleID: t.BundleID, LedgerSeq: seq[t.BundleID]})
	}
	return out, nil
}

func opt(ok bool, s string) string {
	if ok {
		return s
	}
	return ""
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
