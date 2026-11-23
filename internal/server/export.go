package server

import (
	"archive/tar"
	"bytes"
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
	"net/http"
	"os"
	"path/filepath"
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
// every later export, not in the export it records. An export that cannot be recorded is not handed out.

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

// exportFormat names the export bundle's manifest member "export" (an extension member: v1 verifiers ignore it, §2).
const exportFormat = "gaia-evidence-export"

// ExportManifest describes an evidence export in its bundle's manifest (member "export").
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
	Files         int          `json:"files"`             // files SHA256SUMS lists
	SumsSHA256    string       `json:"sha256sums_sha256"` // the hash of the export's SHA256SUMS
	SumsKeyID     string       `json:"sha256sums_key_id"` // the key that signed it (SHA256SUMS.sig)
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
	Kind             string `json:"kind"` // all: the whole ledger and every bundle
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
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return 0, nil, err
	}
	root := "gaia-evidence-" + now.Format("20060102T150405Z")
	bundleID := "export-" + now.Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	sums := map[string]string{}
	add := func(name string, body []byte) error {
		sums[name] = sha256Hex(body)
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + name, Mode: 0o644, Size: int64(len(body)), ModTime: now}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	if err := add("anchor.pub", []byte(base64.StdEncoding.EncodeToString(anchor)+"\n")); err != nil {
		return 0, nil, err
	}
	if err := add("keys/evidence-key-current.pub", current); err != nil {
		return 0, nil, err
	}
	if err := add(evidence.ExportLedgerFile, ledger); err != nil {
		return 0, nil, err
	}
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
		return add(evidence.ExportBundlesDir+"/"+filepath.ToSlash(rel), b)
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
		if err := add(name, rs[0].Report); err != nil {
			return 0, nil, err
		}
		report = fmt.Sprintf("%-26s the newest daily report (%s)", name, rs[0].GeneratedAt)
		if string(rs[0].LedgerHead) != "null" {
			head := fmt.Sprintf("ledger-head-%d.json", rs[0].ID)
			if err := add(head, append(append([]byte{}, rs[0].LedgerHead...), '\n')); err != nil {
				return 0, nil, err
			}
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
	if err := add("restore-tests.json", append(tb, '\n')); err != nil {
		return 0, nil, err
	}
	if err := add("SPEC.md", spec.EvidenceV1); err != nil {
		return 0, nil, err
	}
	signer := evidence.KeyID(key.Public().(ed25519.PublicKey))
	if err := add("README.txt", []byte(exportReadme(exportReadmeData{made: now, version: s.version, anchorID: evidence.KeyID(anchor),
		signerID: signer, ledgerBytes: len(ledger), entries: span.Entries, bundles: bundles, report: report, head: verifyHead,
		bundleID: bundleID}))); err != nil {
		return 0, nil, err
	}
	sumsBytes := evidence.SumsLines(sums)
	sig, err := evidence.SignSums(sumsBytes, key, now)
	if err != nil {
		return 0, nil, err
	}
	files := len(sums)
	if err := add(evidence.SumsFile, sumsBytes); err != nil {
		return 0, nil, err
	}
	if err := add(evidence.SumsSignatureFile, sig); err != nil {
		return 0, nil, err
	}
	if err := tw.Close(); err != nil {
		return 0, nil, err
	}
	if err := gz.Close(); err != nil {
		return 0, nil, err
	}
	via := "session"
	if c.token != nil {
		via = "api_token"
	}
	m := ExportManifest{Format: exportFormat, FormatVersion: 1, Name: root, Made: now.Format(time.RFC3339), Installation: hostname(),
		ServerVersion: s.version, By: ExportBy{User: c.user.Username, UserID: c.user.ID, Role: c.user.Role, Via: via},
		Scope:  ExportScope{Kind: "all", LedgerEntries: span.Entries, LedgerHeadSHA256: span.HeadSHA256, Bundles: bundles, DailyReport: reportID},
		Period: ExportPeriod{From: span.From, To: span.To}, Files: files, SumsSHA256: sha256Hex(sumsBytes), SumsKeyID: signer}
	rec, err := s.recordExport(ctx, bundleID, m, sumsBytes, sig, key, now)
	if err != nil {
		return 0, nil, fmt.Errorf("the export could not be recorded in the evidence ledger, so it is not handed out (ADR-007 C7): %w", err)
	}
	c.detail = fmt.Sprintf("full evidence export: %d bundles, ledger %d entries (%d bytes); SHA256SUMS %s signed by key %s; recorded as bundle %s, ledger entry %d",
		bundles, span.Entries, len(ledger), m.SumsSHA256, signer, rec.BundleID, rec.LedgerSeq)
	c.raw = true
	c.w.Header().Set("Content-Type", "application/gzip")
	c.w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", root+".tar.gz"))
	c.w.Header().Set("X-Gaia-Export-Bundle", rec.BundleID)
	c.w.WriteHeader(http.StatusOK)
	_, err = c.w.Write(buf.Bytes())
	return http.StatusOK, nil, err
}

// recordExport seals the export's record, appends it to the ledger and records it in the catalog. The bundle holds
// the export's SHA256SUMS and its signature; the manifest's subject (strings: for people, and what a v1 verifier
// reads) and its member "export" (ExportManifest) describe the export.
func (s *Server) recordExport(ctx context.Context, bundleID string, m ExportManifest, sums, sig []byte, key ed25519.PrivateKey, now time.Time) (store.EvidenceBundle, error) {
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
	sum, err := evidence.SealExt(dir, bundleID, subject, map[string]any{"export": m}, key, now)
	if err != nil {
		return store.EvidenceBundle{}, fmt.Errorf("sealing %s: %w", bundleID, err)
	}
	e, err := evidence.Append(s.cfg.ledgerPath(), evidence.Entry{BundleID: bundleID, BundleSHA256: sum, Subject: "evidence export by " + m.By.User,
		Verdict: evidence.OK, Time: now}, key)
	if err != nil {
		// The record of an export that is not handed out is kept only if the entry was written after all (the flush
		// failed): a bundle without its entry would be bundle_unlisted, an entry without its bundle bundle_missing.
		seq, serr := evidence.BundleEntry(s.cfg.ledgerPath(), bundleID)
		sealed = serr != nil || seq > 0
		if serr != nil {
			s.log.Error("reading the ledger after a failed append", "bundle", bundleID, "error", serr)
		}
		if _, cerr := s.checkLedger(context.WithoutCancel(ctx), "system", "appending bundle "+bundleID+" failed: "+err.Error(), store.LedgerCheckFull); cerr != nil {
			s.log.Error("ledger check", "error", cerr)
		}
		// The caller is told why (a ledger that is not intact refuses entries; the ledger check now says what is wrong).
		return store.EvidenceBundle{}, fmt.Errorf("%w: the evidence ledger refused the entry: %v", store.ErrConflict, err)
	}
	sealed = true
	return s.db.AddEvidence(context.WithoutCancel(ctx), store.EvidenceBundle{BundleID: bundleID, LedgerSeq: e.Seq, BundleSHA256: sum, Verdict: evidence.OK})
}

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
	bundleID           string
}

// exportReadme is the export's README.txt. Its first verification step stays the comparison of the key_ids with a
// daily report received outside the export (ADR-007 C7: the signature is integrity in transit, not a trust anchor).
func exportReadme(d exportReadmeData) string {
	return fmt.Sprintf(`Evidence export of Gaia installation %s, %s (server version %s).

Contents
  anchor.pub                 the ledger's first key: the trust anchor, key_id %s
  keys/                      the installation's current public key
  ledger.jsonl               the evidence ledger, %d entries, %d bytes
  bundles/                   %d evidence bundles (restore tests, migration checks, records of earlier exports)
  %s
  restore-tests.json         every restore test: VM, point, time, verdict, measured RTO against its target, bundle
  SPEC.md                    Restore Evidence Format, version 1 (CC0): what everything here means and how to verify it
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

This export is recorded
  Gaia recorded this export as evidence bundle %s: its manifest names who made it, its scope and period, and
  the SHA-256 of this SHA256SUMS; its ledger entry follows entry %d. The record is made once the export is complete,
  so it is not in this export: it is in the server's ledger and in every later export, where
  gaia evidence verify -export lists the exports the ledger records.
`, hostname(), d.made.Format(time.RFC3339), d.version, d.anchorID, d.entries, d.ledgerBytes, d.bundles, d.report, d.signerID,
		opt(d.head != "", " and the daily report's head ("+d.head+")"), opt(d.head != "", " -head "+d.head), opt(d.head != "", " --head "+d.head),
		opt(d.head != "", ",\n  and the ledger still holds the head the daily report recorded"), d.bundleID, d.entries)
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
