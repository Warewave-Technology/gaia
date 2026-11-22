package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An incremental check (ADR-007 C14) verifies only the entries after the last checked one, and the link to it: a
// change of an earlier entry, a cut ledger, or damage in a new bundle is found; damage in an old bundle is left for the
// full check, which still finds it.
func TestVerifyLedgerSince(t *testing.T) {
	a, apub, _ := keys(t)
	root := t.TempDir()
	ledger := filepath.Join(root, "ledger.jsonl")
	add := func(id string) {
		t.Helper()
		dir := bundle(t, root, id, a)
		if _, err := Append(ledger, Entry{BundleID: id, BundleSHA256: VerifyBundle(dir, apub).SHA256, Subject: "vm1", Verdict: "OK"}, a); err != nil {
			t.Fatal(err)
		}
	}
	add("b1")
	add("b2")
	full, err := VerifyLedger(ledger, root, apub)
	if err != nil || full.Verdict != OK || full.Entries != 3 {
		t.Fatalf("full: %+v %v", full, err)
	}
	head := ledgerLastHash(t, ledger)
	add("b3")
	r, err := VerifyLedgerSince(ledger, root, apub, nil, 3, head)
	if err != nil || r.Verdict != OK || r.Entries != 4 || len(r.Bundles) != 1 || r.Bundles[0].BundleID != "b3" {
		t.Fatalf("incremental: %+v %v", r, err)
	}
	// Damage in an old bundle: not the incremental check's business; the full check finds it.
	os.WriteFile(filepath.Join(root, "b1", "result.json"), []byte(`{"verdict":"CRITICAL"}`), 0o644)
	if r, _ := VerifyLedgerSince(ledger, root, apub, nil, 3, head); r.Verdict != OK {
		t.Fatalf("incremental with an old bundle changed: %s %v", r.Verdict, r.AllFindings())
	}
	if r, _ := VerifyLedger(ledger, root, apub); r.Verdict != Critical {
		t.Fatalf("full with an old bundle changed: %s", r.Verdict)
	}
	os.RemoveAll(filepath.Join(root, "b1"))
	bundle(t, root, "b1", a)
	// Damage in a new bundle is found.
	os.WriteFile(filepath.Join(root, "b3", "result.json"), []byte(`{"verdict":"CRITICAL"}`), 0o644)
	if r, _ := VerifyLedgerSince(ledger, root, apub, nil, 3, head); r.Verdict != Critical {
		t.Fatalf("incremental with a new bundle changed: %s", r.Verdict)
	}
	// The link: a recorded hash the ledger no longer has (an earlier entry rewritten), or a cut ledger.
	if r, _ := VerifyLedgerSince(ledger, root, apub, nil, 3, strings.Repeat("0", 64)); r.Verdict != Critical || !hasCode(r, CodeChainBroken) {
		t.Fatalf("another head: %s %+v", r.Verdict, r.Findings)
	}
	if r, _ := VerifyLedgerSince(ledger, root, apub, nil, 9, head); r.Verdict != Critical || !hasCode(r, CodeLedgerTruncated) {
		t.Fatalf("cut: %s %+v", r.Verdict, r.Findings)
	}
	// A new entry with a bad signature is found; an old one's signature is not checked again, but its bytes are bound
	// by the chain and the recorded hash.
	b, _ := os.ReadFile(ledger)
	lines := strings.SplitAfter(string(b), "\n")
	bad := strings.Replace(lines[3], `"sig":"`, `"sig":"A`, 1)
	os.WriteFile(ledger, []byte(lines[0]+lines[1]+lines[2]+bad), 0o644)
	if r, _ := VerifyLedgerSince(ledger, root, apub, nil, 3, head); r.Verdict != Critical || !hasCode(r, CodeEntrySignatureInvalid) {
		t.Fatalf("a bad new signature: %s %+v", r.Verdict, r.Findings)
	}
}

func ledgerLastHash(t *testing.T, ledger string) string {
	t.Helper()
	b, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	s := scanLedger(b, nil, false)
	return s.last
}

func hasCode(r LedgerReport, code string) bool {
	for _, f := range r.AllFindings() {
		if f.Code == code {
			return true
		}
	}
	return false
}
