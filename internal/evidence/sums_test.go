package evidence

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// makeExport writes a small export directory like gaia server's (ADR-007 C7): a ledger with two bundles, one of them
// the record of an earlier export (a manifest with the extension member "export"), other files, SHA256SUMS signed
// by key.
func makeExport(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "gaia-evidence-x")
	bdir := filepath.Join(dir, ExportBundlesDir)
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, ExportLedgerFile)
	mb, err := os.ReadFile(filepath.Join(bundle(t, bdir, "b1", key), ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	h := sha256Hex(mb)
	if _, err := Append(ledger, Entry{BundleID: "b1", BundleSHA256: h, Subject: "vm1", Verdict: "OK"}, key); err != nil {
		t.Fatal(err)
	}
	ex := filepath.Join(bdir, "export-1")
	os.MkdirAll(filepath.Join(ex, "export"), 0o755)
	os.WriteFile(filepath.Join(ex, "export", SumsFile), []byte(strings.Repeat("0", 64)+"  README.txt\n"), 0o644)
	sh, err := SealExt(ex, "export-1", map[string]string{"kind": ExportBundleKind, "user": "auditor1", "scope": "all",
		"sha256sums_sha256": strings.Repeat("a", 64), "export": "gaia-evidence-old"},
		map[string]any{"export": map[string]any{"format": "gaia-evidence-export", "format_version": 1, "files": 3, "by": map[string]any{"user": "auditor1"}}},
		key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Append(ledger, Entry{BundleID: "export-1", BundleSHA256: sh, Subject: "evidence export by auditor1", Verdict: "OK"}, key); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "README.txt"), []byte("read me\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "keys"), 0o755)
	os.WriteFile(filepath.Join(dir, "keys", "evidence-key-current.pub"), []byte("k\n"), 0o644)
	_, hb, err := HeadStatement(ledger, key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "ledger-head-1.json"), append(hb, '\n'), 0o644)
	resign(t, dir, key)
	return dir
}

// resign writes SHA256SUMS of the directory's files and signs it with key.
func resign(t *testing.T, dir string, key ed25519.PrivateKey) {
	t.Helper()
	sums := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if err != nil || d.IsDir() || rel == SumsFile || rel == SumsSignatureFile {
			return err
		}
		b, _ := os.ReadFile(p)
		h := sha256.Sum256(b)
		sums[rel] = hex.EncodeToString(h[:])
		return nil
	})
	s := SumsLines(sums)
	sig, err := SignSums(s, key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, SumsFile), s, 0o644)
	os.WriteFile(filepath.Join(dir, SumsSignatureFile), sig, 0o644)
}

func copyDir(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestExportVerification(t *testing.T) {
	key, pub, _ := keys(t)
	other, _, _ := keys(t)
	good := makeExport(t, key)
	verify := func(dir string, anchor ed25519.PublicKey) ExportReport {
		t.Helper()
		r, err := VerifyExport(dir, anchor, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := verify(good, pub)
	if r.Verdict != OK || !r.Sums.SignatureValid || r.Sums.Listed != r.Sums.Matching || r.Sums.Listed < 8 || r.Sums.KeyID != KeyID(pub) ||
		len(r.Ledger.Heads) != 1 || r.Ledger.Heads[0].Result != OK || len(r.HeadFiles) != 1 {
		t.Fatalf("a good export: %+v %v", r.Sums, r.AllFindings())
	}
	// The record of an earlier export is an ordinary v1 bundle (its extension member is ignored) and is listed.
	if len(r.Exports) != 1 || r.Exports[0].BundleID != "export-1" || r.Exports[0].LedgerSeq != 3 || r.Exports[0].User != "auditor1" ||
		r.Exports[0].SumsSHA256 != strings.Repeat("a", 64) || r.Exports[0].Verdict != OK {
		t.Fatalf("recorded exports: %+v", r.Exports)
	}
	mb, _ := os.ReadFile(filepath.Join(good, ExportBundlesDir, "export-1", ManifestFile))
	var m map[string]json.RawMessage
	if json.Unmarshal(mb, &m) != nil || m["export"] == nil {
		t.Fatalf("the extension member is not in the manifest: %s", mb)
	}
	if b := VerifyBundle(filepath.Join(good, ExportBundlesDir, "export-1"), pub); b.Verdict != OK {
		t.Fatalf("the export record as a bundle: %v", b.Findings)
	}
	if _, err := SealExt(t.TempDir(), "x", nil, map[string]any{"files": 1}, key, time.Now()); err == nil {
		t.Fatal("an extension member named like a §5.2 member was accepted")
	}

	cases := []struct {
		name    string
		change  func(dir string)
		anchor  ed25519.PublicKey
		verdict string
		code    string
	}{
		{"no anchor", func(string) {}, nil, Unknown, CodeAnchorMissing},
		{"file changed", func(d string) { os.WriteFile(filepath.Join(d, "README.txt"), []byte("read me, changed\n"), 0o644) }, pub, Critical, CodeSumsFileModified},
		{"SHA256SUMS changed with the file", func(d string) {
			os.WriteFile(filepath.Join(d, "README.txt"), []byte("other\n"), 0o644)
			b, _ := os.ReadFile(filepath.Join(d, SumsFile))
			h := sha256.Sum256([]byte("other\n"))
			lines := strings.Split(string(b), "\n")
			for i, l := range lines {
				if strings.HasSuffix(l, "  README.txt") {
					lines[i] = hex.EncodeToString(h[:]) + "  README.txt"
				}
			}
			os.WriteFile(filepath.Join(d, SumsFile), []byte(strings.Join(lines, "\n")), 0o644)
		}, pub, Critical, CodeSumsSignatureInvalid},
		{"signature missing", func(d string) { os.Remove(filepath.Join(d, SumsSignatureFile)) }, pub, Unknown, CodeSumsSignatureMissing},
		{"signed by another key", func(d string) { resign(t, d, other) }, pub, Critical, CodeSumsKeyMismatch},
		{"signature not JSON", func(d string) { os.WriteFile(filepath.Join(d, SumsSignatureFile), []byte("c2ln\n"), 0o644) }, pub, Critical, CodeSumsSignatureMalformed},
		{"signature bytes changed", func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, SumsSignatureFile))
			var s SumsSignature
			json.Unmarshal(b, &s)
			sig := []byte(s.Signature)
			if sig[0] = 'A'; s.Signature[0] == 'A' {
				sig[0] = 'B'
			}
			s.Signature = string(sig)
			out, _ := json.Marshal(s)
			os.WriteFile(filepath.Join(d, SumsSignatureFile), out, 0o644)
		}, pub, Critical, CodeSumsSignatureInvalid},
		{"unsupported signature version", func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, SumsSignatureFile))
			os.WriteFile(filepath.Join(d, SumsSignatureFile), []byte(strings.Replace(string(b), `"format_version": 1`, `"format_version": 2`, 1)), 0o644)
		}, pub, Unknown, CodeSumsSignatureUnsupported},
		{"file added", func(d string) { os.WriteFile(filepath.Join(d, "extra.txt"), []byte("x"), 0o644) }, pub, Critical, CodeSumsFileUnlisted},
		{"file removed", func(d string) { os.Remove(filepath.Join(d, "keys", "evidence-key-current.pub")) }, pub, Critical, CodeSumsFileMissing},
		{"SHA256SUMS missing", func(d string) { os.Remove(filepath.Join(d, SumsFile)) }, pub, Critical, CodeSumsMissing},
		{"SHA256SUMS line malformed", func(d string) {
			b, _ := os.ReadFile(filepath.Join(d, SumsFile))
			os.WriteFile(filepath.Join(d, SumsFile), append(b, "nonsense ../x\n"...), 0o644)
		}, pub, Critical, CodeSumsMalformed},
		{"symbolic link", func(d string) { os.Symlink("README.txt", filepath.Join(d, "link")) }, pub, Critical, CodeSumsFileTypeInvalid},
		{"bundle file changed and SHA256SUMS signed again", func(d string) {
			os.WriteFile(filepath.Join(d, ExportBundlesDir, "b1", "serial.log"), []byte("changed\n"), 0o644)
			resign(t, d, key) // a thief with the key: the bundle's manifest still catches it
		}, pub, Critical, CodeFileModified},
	}
	for _, c := range cases {
		d := copyDir(t, good)
		c.change(d)
		r := verify(d, c.anchor)
		var cs []string
		for _, f := range r.AllFindings() {
			cs = append(cs, f.Code)
		}
		if r.Verdict != c.verdict || !slices.Contains(cs, c.code) {
			t.Errorf("%s: want %s with %s, got %s %v", c.name, c.verdict, c.code, r.Verdict, r.AllFindings())
		}
	}
	// A directory that is not one: no verdict.
	if _, err := VerifyExport(filepath.Join(good, "README.txt"), pub, nil); err == nil {
		t.Fatal("a file verified as an export")
	}
}
