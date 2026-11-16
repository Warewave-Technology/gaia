package evidence

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func keys(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	t.Helper()
	dir := t.TempDir()
	priv, pub := filepath.Join(dir, "key.pem"), filepath.Join(dir, "pub.pem")
	id, err := GenerateKey(priv, pub)
	if err != nil {
		t.Fatal(err)
	}
	k, err := LoadPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if KeyID(p) != id || !p.Equal(k.Public()) {
		t.Fatal("key round trip")
	}
	if fi, _ := os.Stat(priv); fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v", fi.Mode())
	}
	if _, err := GenerateKey(priv, pub); err == nil {
		t.Fatal("GenerateKey overwrote an existing key")
	}
	return k, p, dir
}

// bundle makes and seals a small bundle in root/id.
func bundle(t *testing.T, root, id string, key ed25519.PrivateKey) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "shots"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"result.json":    `{"verdict":"OK"}`,
		"serial.log":     "booting\nlogin: \n",
		"shots/boot.png": "\x89PNG fake",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Seal(dir, id, map[string]string{"vm": "vm1", "verdict": "OK"}, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 64 {
		t.Fatalf("bundle hash %q", h)
	}
	return dir
}

func flip(t *testing.T, path string, i int) (restore func()) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	orig := append([]byte(nil), b...)
	b[i] ^= 0x01
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return func() { os.WriteFile(path, orig, 0o644) }
}

func codes(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestBundleEveryByteIsCovered(t *testing.T) {
	key, pub, _ := keys(t)
	dir := bundle(t, t.TempDir(), "b1", key)
	if r := VerifyBundle(dir, pub); r.Verdict != OK || r.Files != 3 || r.BundleID != "b1" || r.Format != "v1" || r.KeyID != KeyID(pub) {
		t.Fatalf("fresh bundle: %+v", r)
	}
	for _, name := range []string{ManifestFile, SignatureFile, "result.json", "serial.log", "shots/boot.png"} {
		p := filepath.Join(dir, name)
		fi, _ := os.Stat(p)
		for i := range int(fi.Size()) {
			undo := flip(t, p, i)
			// A manifest whose format string changed is not one a verifier can judge: UNKNOWN on its own,
			// CRITICAL in a ledger (the recorded manifest hash no longer matches); never OK.
			if r := VerifyBundle(dir, pub); r.Verdict == OK || (r.Verdict == Unknown && !slices.Equal(codes(r.Findings), []string{CodeFormatUnsupported})) {
				t.Fatalf("%s: flipped byte %d not detected: %+v", name, i, r)
			}
			undo()
		}
	}
	if r := VerifyBundle(dir, pub); r.Verdict != OK {
		t.Fatalf("restored bundle: %v", r.Problems)
	}
}

func TestBundleFindings(t *testing.T) {
	key, pub, _ := keys(t)
	dir := bundle(t, t.TempDir(), "b1", key)
	check := func(name string, pub ed25519.PublicKey, want ...string) {
		t.Helper()
		if r := VerifyBundle(dir, pub); !slices.Equal(codes(r.Findings), want) {
			t.Fatalf("%s: findings %v, want %v", name, r.Findings, want)
		}
	}
	os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o644)
	check("added file", pub, CodeFileUnlisted)
	os.Remove(filepath.Join(dir, "extra.txt"))
	os.Symlink("result.json", filepath.Join(dir, "link"))
	check("symlink", pub, CodeFileTypeInvalid)
	os.Remove(filepath.Join(dir, "link"))
	os.Remove(filepath.Join(dir, "serial.log"))
	check("removed file", pub, CodeFileMissing)
	_, other, _ := keys(t)
	check("another key", other, CodeFileMissing, CodeKeyMismatch)
	check("no key", nil, CodeAnchorMissing, CodeFileMissing)
	if r := VerifyBundle(dir, nil); r.Verdict != Critical {
		t.Fatalf("a missing file with no key is still CRITICAL: %+v", r)
	}
}

func TestLedgerAndRotation(t *testing.T) {
	a, apub, _ := keys(t)
	b, bpub, _ := keys(t)
	root := t.TempDir()
	ledger := filepath.Join(root, "ledger.jsonl")
	appendBundle := func(id string, key ed25519.PrivateKey) (Entry, error) {
		dir := bundle(t, root, id, key)
		return Append(ledger, Entry{BundleID: id, BundleSHA256: VerifyBundle(dir, key.Public().(ed25519.PublicKey)).SHA256, Subject: "vm1", Verdict: "OK"}, key)
	}
	e1, err := appendBundle("b1", a)
	if err != nil || e1.Seq != 2 {
		t.Fatalf("first entry after genesis: %+v %v", e1, err)
	}
	if _, err := appendBundle("b2", a); err != nil {
		t.Fatal(err)
	}
	if _, err := Rotate(ledger, b, apub); err == nil {
		t.Fatal("a rotation signed by a key that is not the current one was written")
	}
	rot, err := Rotate(ledger, a, bpub)
	if err != nil || rot.Seq != 4 {
		t.Fatalf("rotation %+v %v", rot, err)
	}
	if _, err := appendBundle("b3", a); err == nil || !strings.Contains(err.Error(), "rotate first") {
		t.Fatalf("the old key appended after the rotation: %v", err)
	}
	os.RemoveAll(filepath.Join(root, "b3"))
	if e, err := appendBundle("b3", b); err != nil || e.Seq != 5 || e.KeyID != KeyID(bpub) {
		t.Fatalf("new key: %+v %v", e, err)
	}
	r, err := VerifyLedger(ledger, root, apub)
	if err != nil || r.Verdict != OK || r.Entries != 5 || len(r.Bundles) != 3 || !slices.Equal(r.Keys, []string{KeyID(apub), KeyID(bpub)}) {
		t.Fatalf("ledger with a rotation: %+v %v", r, err)
	}
	if r.Bundles[2].KeyID != KeyID(bpub) || r.Bundles[0].KeyID != KeyID(apub) {
		t.Fatalf("bundle keys %+v", r.Bundles)
	}
	// The new key is not a trust anchor: the ledger's genesis key is.
	if r, _ := VerifyLedger(ledger, root, bpub); !slices.Contains(codes(r.Findings), CodeAnchorMismatch) || r.Verdict != Critical {
		t.Fatalf("wrong anchor: %+v", r.Findings)
	}
	if r, _ := VerifyLedger(ledger, root, nil); r.Verdict != Unknown || !slices.Equal(codes(r.AllFindings()), []string{CodeAnchorMissing}) {
		t.Fatalf("no anchor: %+v", r.AllFindings())
	}

	// Every single byte of the ledger matters.
	fi, _ := os.Stat(ledger)
	for i := range int(fi.Size()) {
		undo := flip(t, ledger, i)
		if r, _ := VerifyLedger(ledger, root, apub); r.Verdict != Critical {
			t.Fatalf("flipped ledger byte %d not detected: %+v", i, r.Findings)
		}
		undo()
	}

	// Dropping or reordering lines breaks the chain.
	orig, _ := os.ReadFile(ledger)
	lines := strings.SplitAfter(string(orig), "\n")
	for name, mutated := range map[string]string{
		"drop a bundle entry": lines[0] + lines[2] + lines[3] + lines[4],
		"drop the last":       lines[0] + lines[1] + lines[2] + lines[3],
		"reorder":             lines[0] + lines[2] + lines[1] + lines[3] + lines[4],
		"cut mid-line":        lines[0] + lines[1] + lines[2] + lines[3] + lines[4][:40],
	} {
		os.WriteFile(ledger, []byte(mutated), 0o644)
		if r, _ := VerifyLedger(ledger, root, apub); r.Verdict != Critical {
			t.Fatalf("%s not detected", name)
		}
	}
	os.WriteFile(ledger, orig, 0o644)

	// A bundle replaced by another validly signed bundle is caught by the recorded manifest hash.
	os.RemoveAll(filepath.Join(root, "b2"))
	bundle(t, root, "b2", a)
	if r, _ := VerifyLedger(ledger, root, apub); !slices.Equal(codes(r.AllFindings()), []string{CodeBundleHashMismatch}) {
		t.Fatalf("replaced bundle: %+v", r.AllFindings())
	}

	// Append refuses to extend a broken ledger.
	undo := flip(t, ledger, 20)
	if _, err := Append(ledger, Entry{BundleID: "b9", BundleSHA256: strings.Repeat("0", 64)}, b); err == nil {
		t.Fatal("appended to a broken ledger")
	}
	undo()
}

// The bundles committed before the specification (v0) still verify, through the read-only v0 path.
func TestV0Evidence(t *testing.T) {
	base := "../../docs/reports/evidence/2026-11-16-T8"
	pub, err := LoadPublicKey(filepath.Join(base, "evidence-pub.pem"))
	if err != nil {
		t.Skip("no committed v0 evidence:", err)
	}
	r, err := VerifyLedger(filepath.Join(base, "ledger.jsonl"), filepath.Join(base, "evidence"), pub)
	if err != nil || r.Verdict != OK || r.Format != "v0" || r.Entries != 2 || r.Bundles[0].Format != "v0" {
		t.Fatalf("v0 ledger: %+v %v", r, err)
	}
	if _, err := Append(filepath.Join(t.TempDir(), "x"), Entry{}, nil); err == nil {
		t.Fatal("an entry without a bundle id was accepted")
	}
	tmp := filepath.Join(t.TempDir(), "ledger.jsonl")
	b, _ := os.ReadFile(filepath.Join(base, "ledger.jsonl"))
	os.WriteFile(tmp, b, 0o644)
	k, _, _ := keys(t)
	if _, err := Append(tmp, Entry{BundleID: "x", BundleSHA256: strings.Repeat("0", 64)}, k); err == nil || !strings.Contains(err.Error(), "v0") {
		t.Fatalf("appended to a v0 ledger: %v", err)
	}
}

func TestSurrogates(t *testing.T) {
	for in, ok := range map[string]bool{
		`{"a":"x\\ud800y"}`:    true, // an escaped backslash, then text
		`{"a":"\ud83d\ude00"}`: true, `{"a":"\u00e9"}`: true, `{"a":"\"\\"}`: true,
		`{"a":"\ud800"}`: false, `{"a":"\ude00"}`: false, `{"a":"\ud800\u0041"}`: false, `{"a":"\ud800\ud800\udc00"}`: false,
	} {
		if err := surrogates([]byte(in)); (err == nil) != ok {
			t.Errorf("%s: %v", in, err)
		}
	}
}
