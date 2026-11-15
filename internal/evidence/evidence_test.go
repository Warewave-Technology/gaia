package evidence

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func keys(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	t.Helper()
	dir := t.TempDir()
	priv, pub := filepath.Join(dir, "key.pem"), filepath.Join(dir, "pub.pem")
	fp, err := GenerateKey(priv, pub)
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
	if Fingerprint(p) != fp || !p.Equal(k.Public()) {
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

func TestBundleEveryByteIsCovered(t *testing.T) {
	key, pub, _ := keys(t)
	dir := bundle(t, t.TempDir(), "b1", key)
	if r := VerifyBundle(dir, pub); len(r.Problems) != 0 || r.Files != 3 || r.BundleID != "b1" {
		t.Fatalf("fresh bundle: %+v", r)
	}
	for _, name := range []string{ManifestFile, SignatureFile, "result.json", "serial.log", "shots/boot.png"} {
		p := filepath.Join(dir, name)
		fi, _ := os.Stat(p)
		for i := range int(fi.Size()) {
			if name == SignatureFile && i == int(fi.Size())-1 {
				continue // the trailing newline is outside the signature on purpose (TrimSpace)
			}
			undo := flip(t, p, i)
			if r := VerifyBundle(dir, pub); len(r.Problems) == 0 {
				t.Fatalf("%s: flipped byte %d not detected", name, i)
			}
			undo()
		}
	}
	if r := VerifyBundle(dir, pub); len(r.Problems) != 0 {
		t.Fatalf("restored bundle: %v", r.Problems)
	}
}

func TestBundleAddedRemovedWrongKey(t *testing.T) {
	key, pub, _ := keys(t)
	dir := bundle(t, t.TempDir(), "b1", key)
	os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("x"), 0o644)
	if r := VerifyBundle(dir, pub); !hasProblem(r.Problems, "extra.txt: not in the manifest") {
		t.Fatalf("added file: %v", r.Problems)
	}
	os.Remove(filepath.Join(dir, "extra.txt"))
	os.Remove(filepath.Join(dir, "serial.log"))
	if r := VerifyBundle(dir, pub); !hasProblem(r.Problems, "serial.log: listed but missing") {
		t.Fatalf("removed file: %v", r.Problems)
	}
	_, other, _ := keys(t)
	dir2 := bundle(t, t.TempDir(), "b2", key)
	if r := VerifyBundle(dir2, other); !hasProblem(r.Problems, "signature does not match") {
		t.Fatalf("wrong key: %v", r.Problems)
	}
}

func TestLedger(t *testing.T) {
	key, pub, _ := keys(t)
	root := t.TempDir()
	ledger := filepath.Join(root, "ledger.jsonl")
	var hashes []string
	for _, id := range []string{"b1", "b2", "b3"} {
		dir := bundle(t, root, id, key)
		r := VerifyBundle(dir, pub)
		e, err := Append(ledger, Entry{BundleID: id, BundleSHA256: r.SHA256, Subject: "vm1", Verdict: "OK"}, key)
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, e.EntrySHA256)
		if e.Seq != len(hashes) || (len(hashes) > 1 && e.Prev != hashes[len(hashes)-2]) {
			t.Fatalf("entry %+v", e)
		}
	}
	r, err := VerifyLedger(ledger, root, pub)
	if err != nil || !r.Intact || r.Entries != 3 || len(r.Bundles) != 3 {
		t.Fatalf("fresh ledger: %+v %v", r, err)
	}

	// Every single byte of the ledger matters.
	fi, _ := os.Stat(ledger)
	for i := range int(fi.Size()) {
		if i == int(fi.Size())-1 {
			continue // the final newline only ends the last line
		}
		undo := flip(t, ledger, i)
		if r, _ := VerifyLedger(ledger, root, pub); r.Intact {
			t.Fatalf("flipped ledger byte %d not detected", i)
		}
		undo()
	}

	// Dropping or reordering lines breaks the chain.
	orig, _ := os.ReadFile(ledger)
	lines := strings.SplitAfter(string(orig), "\n")
	for name, mutated := range map[string]string{
		"drop first": lines[1] + lines[2],
		"drop last":  lines[0] + lines[1], // bundle b3 is still there, without an entry
		"reorder":    lines[1] + lines[0] + lines[2],
	} {
		os.WriteFile(ledger, []byte(mutated), 0o644)
		if r, _ := VerifyLedger(ledger, root, pub); r.Intact {
			t.Fatalf("%s not detected", name)
		}
	}
	os.WriteFile(ledger, orig, 0o644)

	// A bundle replaced by another validly signed bundle is caught by the recorded manifest hash.
	os.RemoveAll(filepath.Join(root, "b2"))
	bundle(t, root, "b2", key)
	if r, _ := VerifyLedger(ledger, root, pub); r.Intact || !hasProblem(r.Bundles[1].Problems, "ledger seq 2 recorded") {
		t.Fatalf("replaced bundle: %+v", r.Bundles[1])
	}

	// Append refuses to extend a broken ledger.
	undo := flip(t, ledger, 20)
	if _, err := Append(ledger, Entry{BundleID: "b4", BundleSHA256: "x"}, key); err == nil {
		t.Fatal("appended to a broken ledger")
	}
	undo()
}

func hasProblem(ps []string, sub string) bool {
	for _, p := range ps {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}
