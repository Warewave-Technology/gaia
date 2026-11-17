package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Warewave-Technology/gaia/docs/spec"
	"github.com/Warewave-Technology/gaia/internal/evidence"
)

// The auditor's evidence export (T20, ADR-007 §7): everything needed to verify the installation's evidence offline,
// without Gaia: every bundle, the ledger, the trust anchor and the current public key, the newest daily report (whose
// signed ledger head shows the ledger was not cut short since), the specification it is all verified against, a
// summary of the restore tests, and how to verify it.

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

func (s *Server) auditExport(c *call) (int, any, error) {
	ctx := c.r.Context()
	now := s.now().UTC()
	c.target = "evidence:export"
	anchor, err := evidence.GenesisKey(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, fmt.Errorf("the ledger's trust anchor: %w", err)
	}
	ledger, err := os.ReadFile(s.cfg.ledgerPath())
	if err != nil {
		return 0, nil, err
	}
	current, err := os.ReadFile(s.cfg.pubPath())
	if err != nil {
		return 0, nil, err
	}
	root := "gaia-evidence-" + now.Format("20060102T150405Z")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	sums := map[string]string{}
	add := func(name string, body []byte) error {
		h := sha256.Sum256(body)
		sums[name] = hex.EncodeToString(h[:])
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
	if err := add("ledger.jsonl", ledger); err != nil {
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
		rel, _ := filepath.Rel(s.cfg.bundlesDir(), p)
		if filepath.Base(rel) == evidence.ManifestFile {
			bundles++
		}
		return add("bundles/"+filepath.ToSlash(rel), b)
	})
	if err != nil {
		return 0, nil, err
	}
	report := "no daily report was made yet: the export cannot show that the ledger was not cut short"
	verifyHead := ""
	if rs, err := s.db.DailyReports(ctx, 1); err != nil {
		return 0, nil, err
	} else if len(rs) == 1 {
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
	keyID := evidence.KeyID(anchor)
	readme := fmt.Sprintf(`Evidence export of Gaia installation %s, %s (server version %s).

Contents
  anchor.pub                 the ledger's first key: the trust anchor, key_id %s
  keys/                      the installation's current public key
  ledger.jsonl               the evidence ledger, %d bytes
  bundles/                   %d evidence bundles (one per restore test)
  %s
  restore-tests.json         every restore test: VM, point, time, verdict, measured RTO against its target, bundle
  SPEC.md                    Restore Evidence Format, version 1 (CC0): what everything here means and how to verify it
  SHA256SUMS                 the hash of every file above

Verify, without Gaia's server
  1. Compare the trust anchor's key_id (above) with the one you recorded when Gaia was installed, or with the
     evidence_key_id in a daily report you received by email. A verifier must not take the anchor from the data it
     verifies (SPEC.md §3).
  2. With Gaia's command-line tool (any machine, no server):
       gaia evidence verify -pubkey anchor.pub -ledger ledger.jsonl -evidence-dir bundles%s
  3. With any other implementation of SPEC.md, for example the independent Python verifier in Gaia's source
     (docs/reports/evidence/2026-11-17-T19/independent-verifier/verify.py, standard library only):
       python3 -I verify.py ledger ledger.jsonl bundles --key anchor.pub%s
  OK means: every entry from the anchor on is signed and chained, every bundle is exactly as sealed, no bundle is
  missing or extra%s.
  A copy of the daily report that reached you by email is a stronger head than the one in this export: give that one
  with -head (--head) as well.
`, hostname(), now.Format(time.RFC3339), s.version, keyID, len(ledger), bundles, report,
		opt(verifyHead != "", " -head "+verifyHead), opt(verifyHead != "", " --head "+verifyHead),
		opt(verifyHead != "", ",\n  and the ledger still holds the head the daily report recorded"))
	if err := add("README.txt", []byte(readme)); err != nil {
		return 0, nil, err
	}
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&sb, "%s  %s\n", sums[n], n)
	}
	if err := add("SHA256SUMS", sb.Bytes()); err != nil {
		return 0, nil, err
	}
	if err := tw.Close(); err != nil {
		return 0, nil, err
	}
	if err := gz.Close(); err != nil {
		return 0, nil, err
	}
	c.detail = fmt.Sprintf("full evidence export: %d bundles, ledger %d bytes", bundles, len(ledger))
	c.raw = true
	c.w.Header().Set("Content-Type", "application/gzip")
	c.w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", root+".tar.gz"))
	c.w.WriteHeader(http.StatusOK)
	_, err = c.w.Write(buf.Bytes())
	return http.StatusOK, nil, err
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
