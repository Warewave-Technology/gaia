package evidence

// Conformance vectors for docs/spec/evidence-v1.md (§10). TestVectors checks that the committed vectors are
// exactly what this generator makes, and that every vector gives exactly its declared verdict and findings.
// Regenerate with:  go test ./internal/evidence -run TestVectors -update
//
// The keys are test keys derived from fixed seeds (below); they sign nothing but these vectors.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite docs/spec/vectors")

const vectorsDir = "../../docs/spec/vectors"

func testKey(name string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("restore-evidence-v1 test key " + name))
	return ed25519.NewKeyFromSeed(seed[:])
}

var (
	keyA = testKey("A")
	keyB = testKey("B")
	t0   = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
)

func pubOf(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }

// Expected is expected.json of a vector.
type Expected struct {
	Description string   `json:"description"`
	Mode        string   `json:"mode"` // bundle or ledger
	Verdict     string   `json:"verdict"`
	Findings    []string `json:"findings"` // the set of finding codes, sorted
}

type vgen struct {
	t   *testing.T
	dir string
}

func (g vgen) must(err error) {
	g.t.Helper()
	if err != nil {
		g.t.Fatal(err)
	}
}

func (g vgen) write(p string, b []byte) {
	g.t.Helper()
	g.must(os.MkdirAll(filepath.Dir(filepath.Join(g.dir, p)), 0o755))
	g.must(os.WriteFile(filepath.Join(g.dir, p), b, 0o644))
}

// bundle writes and seals a small bundle at rel with the given id and verdict.
func (g vgen) bundle(rel, id, verdict string, key ed25519.PrivateKey, created time.Time) string {
	g.t.Helper()
	g.write(rel+"/result.json", []byte(fmt.Sprintf(`{"vm":"web1","verdict":%q}`+"\n", verdict)))
	g.write(rel+"/serial.log", []byte("booting\nweb1 login: \n"))
	g.write(rel+"/screenshots/boot.png", []byte("\x89PNG\r\n\x1a\n(test image)"))
	h, err := SealAt(filepath.Join(g.dir, rel), id, map[string]string{"vm": "web1", "verdict": verdict}, key, created)
	g.must(err)
	return h
}

func (g vgen) anchor(k ed25519.PrivateKey) {
	g.write("anchor.pub", []byte(base64.StdEncoding.EncodeToString(pubOf(k))+"\n"))
}

func (g vgen) ledger() string { return filepath.Join(g.dir, "ledger.jsonl") }

func (g vgen) appendBundle(id string, key ed25519.PrivateKey, at time.Time) {
	g.t.Helper()
	h := g.bundle("bundles/"+id, id, "OK", key, at)
	_, err := Append(g.ledger(), Entry{BundleID: id, BundleSHA256: h, Subject: "web1", Verdict: "OK", Time: at}, key)
	g.must(err)
}

// okLedger: genesis A, bundles b1 and b2 signed by A.
func (g vgen) okLedger() {
	g.appendBundle("b1", keyA, t0)
	g.appendBundle("b2", keyA, t0.Add(time.Minute))
	g.anchor(keyA)
}

// line appends a hand-made, validly signed ledger line.
func (g vgen) line(p any, key ed25519.PrivateKey) string {
	g.t.Helper()
	l, h, err := signLine(p, key)
	g.must(err)
	f, err := os.OpenFile(g.ledger(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	g.must(err)
	defer f.Close()
	_, err = f.Write(l)
	g.must(err)
	return h
}

func genesis(k ed25519.PrivateKey, at time.Time) keyPayload {
	return keyPayload{payloadCommon: common(1, at, "key", KeyID(pubOf(k)), zeroHash), Event: "genesis",
		PublicKey: base64.StdEncoding.EncodeToString(pubOf(k))}
}

func rotation(seq int, old, next ed25519.PrivateKey, prev string, at time.Time) keyPayload {
	return keyPayload{payloadCommon: common(seq, at, "key", KeyID(pubOf(old)), prev), Event: "rotate",
		PublicKey: base64.StdEncoding.EncodeToString(pubOf(next)), NewKeyID: KeyID(pubOf(next))}
}

func bundleEntry(seq int, signer ed25519.PrivateKey, prev, id, sha string, at time.Time) bundlePayload {
	return bundlePayload{payloadCommon: common(seq, at, "bundle", KeyID(pubOf(signer)), prev), BundleID: id, BundleSHA256: sha,
		Subject: "web1", Verdict: "OK"}
}

// resign rewrites a bundle's manifest through edit and signs it again with key.
func (g vgen) resign(rel string, key ed25519.PrivateKey, edit func(m map[string]any)) {
	g.t.Helper()
	dir := filepath.Join(g.dir, rel)
	b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	g.must(err)
	var m map[string]any
	g.must(json.Unmarshal(b, &m))
	edit(m)
	b, err = json.MarshalIndent(m, "", "  ")
	g.must(err)
	b = append(b, '\n')
	g.must(os.WriteFile(filepath.Join(dir, ManifestFile), b, 0o644))
	g.must(os.WriteFile(filepath.Join(dir, SignatureFile), []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, b))+"\n"), 0o644))
}

func (g vgen) replace(p, old, new string) {
	g.t.Helper()
	b, err := os.ReadFile(filepath.Join(g.dir, p))
	g.must(err)
	if !bytes.Contains(b, []byte(old)) {
		g.t.Fatalf("%s: %q not found", p, old)
	}
	g.must(os.WriteFile(filepath.Join(g.dir, p), bytes.Replace(b, []byte(old), []byte(new), 1), 0o644))
}

// head writes a head statement of the ledger as it is now to heads/<name>.json.
func (g vgen) head(name string, key ed25519.PrivateKey, at time.Time) {
	g.t.Helper()
	_, b, err := HeadStatement(g.ledger(), key, at)
	g.must(err)
	g.write("heads/"+name+".json", append(b, '\n'))
}

// headOf writes a hand-made, validly signed head statement to heads/<name>.json.
func (g vgen) headOf(name string, p any, key ed25519.PrivateKey) {
	g.t.Helper()
	pb, err := json.Marshal(p)
	g.must(err)
	b, err := json.Marshal(line{Payload: string(pb), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(key, pb))})
	g.must(err)
	g.write("heads/"+name+".json", append(b, '\n'))
}

// keepLines cuts the ledger to its first n lines.
func (g vgen) keepLines(n int) {
	g.t.Helper()
	b, err := os.ReadFile(g.ledger())
	g.must(err)
	l := strings.SplitAfter(string(b), "\n")
	g.write("ledger.jsonl", []byte(strings.Join(l[:n], "")))
}

type vector struct {
	name, mode, verdict, description string
	findings                         []string
	make                             func(g vgen)
}

var vectors = []vector{
	// --- valid ---
	{"valid-ledger", "ledger", OK, "a genesis entry for key A and two bundles signed by A", nil, func(g vgen) { g.okLedger() }},
	{"valid-rotation", "ledger", OK, "key A signs a bundle, hands over to key B, B signs the next bundle; the anchor is A",
		nil, func(g vgen) {
			g.appendBundle("b1", keyA, t0)
			_, err := RotateAt(g.ledger(), keyA, pubOf(keyB), t0.Add(time.Minute))
			g.must(err)
			g.appendBundle("b2", keyB, t0.Add(2*time.Minute))
			g.anchor(keyA)
		}},
	{"valid-bundle", "bundle", OK, "one bundle signed by A, checked against A", nil, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.anchor(keyA)
	}},
	{"anchor-missing", "ledger", Unknown, "a valid ledger, but no trust anchor is given", []string{CodeAnchorMissing}, func(g vgen) {
		g.okLedger()
		g.must(os.Remove(filepath.Join(g.dir, "anchor.pub")))
	}},

	// --- bundle ---
	{"bundle-manifest-missing", "bundle", Critical, "manifest.json was deleted", []string{CodeManifestMissing}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.must(os.Remove(filepath.Join(g.dir, "bundle", ManifestFile)))
		g.anchor(keyA)
	}},
	{"bundle-manifest-malformed", "bundle", Critical, "manifest.json is not JSON", []string{CodeManifestMalformed}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.write("bundle/"+ManifestFile, []byte("{\"format\": \"restore-evidence\",\n"))
		g.anchor(keyA)
	}},
	{"bundle-manifest-duplicate-member", "bundle", Critical, "manifest.json names \"bundle_id\" twice (validly signed)",
		[]string{CodeManifestMalformed}, func(g vgen) {
			g.bundle("bundle", "b1", "OK", keyA, t0)
			g.replace("bundle/"+ManifestFile, `"bundle_id": "b1",`, `"bundle_id": "b1", "bundle_id": "b2",`)
			b, _ := os.ReadFile(filepath.Join(g.dir, "bundle", ManifestFile))
			g.write("bundle/"+SignatureFile, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(keyA, b))+"\n"))
			g.anchor(keyA)
		}},
	{"bundle-format-unsupported", "bundle", Unknown, "format_version 2 (validly signed): a v1 verifier cannot judge it",
		[]string{CodeFormatUnsupported}, func(g vgen) {
			g.bundle("bundle", "b1", "OK", keyA, t0)
			g.resign("bundle", keyA, func(m map[string]any) { m["format_version"] = 2 })
			g.anchor(keyA)
		}},
	{"bundle-key-mismatch", "bundle", Critical, "the bundle is signed by B, the key given is A", []string{CodeKeyMismatch}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyB, t0)
		g.anchor(keyA)
	}},
	{"bundle-manifest-symlink", "bundle", Critical, "manifest.json is a symbolic link to a valid manifest outside the bundle",
		[]string{CodeManifestMissing}, func(g vgen) {
			g.bundle("bundle", "b1", "OK", keyA, t0)
			g.must(os.Rename(filepath.Join(g.dir, "bundle", ManifestFile), filepath.Join(g.dir, "manifest.real")))
			g.must(os.Symlink("../manifest.real", filepath.Join(g.dir, "bundle", ManifestFile)))
			g.anchor(keyA)
		}},
	{"bundle-signature-missing", "bundle", Critical, "manifest.sig was deleted", []string{CodeSignatureMissing}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.must(os.Remove(filepath.Join(g.dir, "bundle", SignatureFile)))
		g.anchor(keyA)
	}},
	{"bundle-signature-malformed", "bundle", Critical, "manifest.sig is not the base64 of 64 bytes", []string{CodeSignatureMalformed}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.write("bundle/"+SignatureFile, []byte("c2lnbmF0dXJl\n"))
		g.anchor(keyA)
	}},
	{"bundle-signature-invalid", "bundle", Critical, "the subject's verdict was changed from CRITICAL to OK in manifest.json, which was not signed again",
		[]string{CodeSignatureInvalid}, func(g vgen) {
			g.bundle("bundle", "b1", "CRITICAL", keyA, t0)
			g.replace("bundle/"+ManifestFile, `"verdict": "CRITICAL"`, `"verdict": "OK"`)
			g.anchor(keyA)
		}},
	{"bundle-path-invalid", "bundle", Critical, "the manifest (validly signed) lists a path outside the bundle", []string{CodePathInvalid}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.resign("bundle", keyA, func(m map[string]any) {
			m["files"] = append([]any{map[string]any{"path": "../outside.txt", "sha256": strings.Repeat("0", 64), "bytes": 0}}, m["files"].([]any)...)
		})
		g.anchor(keyA)
	}},
	{"bundle-file-missing", "bundle", Critical, "serial.log was deleted", []string{CodeFileMissing}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.must(os.Remove(filepath.Join(g.dir, "bundle", "serial.log")))
		g.anchor(keyA)
	}},
	{"bundle-file-modified", "bundle", Critical, "one byte of result.json was changed", []string{CodeFileModified}, func(g vgen) {
		g.bundle("bundle", "b1", "CRITICAL", keyA, t0)
		g.replace("bundle/result.json", "CRITICAL", "CRITICAk")
		g.anchor(keyA)
	}},
	{"bundle-file-unlisted", "bundle", Critical, "a file was added after sealing", []string{CodeFileUnlisted}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.write("bundle/screenshots/extra.png", []byte("added later"))
		g.anchor(keyA)
	}},
	{"bundle-file-type-invalid", "bundle", Critical, "a symbolic link was added after sealing", []string{CodeFileTypeInvalid}, func(g vgen) {
		g.bundle("bundle", "b1", "OK", keyA, t0)
		g.must(os.Symlink("result.json", filepath.Join(g.dir, "bundle", "link.json")))
		g.anchor(keyA)
	}},

	// --- ledger ---
	{"ledger-missing", "ledger", Critical, "there is no ledger.jsonl", []string{CodeLedgerMissing}, func(g vgen) {
		g.write("bundles/.keep", nil)
		g.anchor(keyA)
	}},
	{"ledger-empty", "ledger", Critical, "ledger.jsonl is empty", []string{CodeLedgerEmpty}, func(g vgen) {
		g.write("ledger.jsonl", nil)
		g.write("bundles/.keep", nil)
		g.anchor(keyA)
	}},
	{"ledger-last-line-cut", "ledger", Critical, "the last line was cut in half: its bundle has no readable entry",
		[]string{CodeBundleUnlisted, CodeEntryMalformed}, func(g vgen) {
			g.okLedger()
			b, _ := os.ReadFile(g.ledger())
			g.write("ledger.jsonl", b[:len(b)-60])
		}},
	{"ledger-entry-malformed", "ledger", Critical, "a validly signed entry lacks bundle_sha256", []string{CodeEntryMalformed}, func(g vgen) {
		h := g.line(genesis(keyA, t0), keyA)
		g.line(struct {
			payloadCommon
			BundleID string `json:"bundle_id"`
			Subject  string `json:"subject"`
			Verdict  string `json:"verdict"`
		}{common(2, t0, "bundle", KeyID(pubOf(keyA)), h), "b1", "web1", "OK"}, keyA)
		g.write("bundles/.keep", nil)
		g.anchor(keyA)
	}},
	{"ledger-entry-surrogate", "ledger", Critical, "an entry's payload holds an unpaired UTF-16 surrogate escape (\\ud800), which has no UTF-8 bytes",
		[]string{CodeEntryMalformed}, func(g vgen) {
			g.line(genesis(keyA, t0), keyA)
			g.line(bundleEntry(2, keyA, strings.Repeat("4", 64), "b1", strings.Repeat("5", 64), t0), keyA)
			g.replace("ledger.jsonl", `\"subject\":\"web1\"`, `\"subject\":\"web\ud8001\"`)
			g.write("bundles/.keep", nil)
			g.anchor(keyA)
		}},
	{"ledger-format-unsupported", "ledger", Unknown, "the last entry (validly signed) has format_version 2",
		[]string{CodeFormatUnsupported}, func(g vgen) {
			h := g.line(genesis(keyA, t0), keyA)
			r := rotation(2, keyA, keyB, h, t0)
			r.FormatVersion = 2
			g.line(r, keyA)
			g.write("bundles/.keep", nil)
			g.anchor(keyA)
		}},
	{"ledger-seq-invalid", "ledger", Critical, "the second entry (validly signed) says seq 5", []string{CodeSeqInvalid}, func(g vgen) {
		h := g.line(genesis(keyA, t0), keyA)
		g.line(bundleEntry(5, keyA, h, "b1", g.bundle("bundles/b1", "b1", "OK", keyA, t0), t0), keyA)
		g.anchor(keyA)
	}},
	{"ledger-chain-broken", "ledger", Critical, "the second entry (validly signed) names the wrong previous entry", []string{CodeChainBroken}, func(g vgen) {
		g.line(genesis(keyA, t0), keyA)
		g.line(bundleEntry(2, keyA, strings.Repeat("1", 64), "b1", g.bundle("bundles/b1", "b1", "OK", keyA, t0), t0), keyA)
		g.anchor(keyA)
	}},
	{"ledger-entry-deleted", "ledger", Critical, "the entry of bundle b1 was deleted from the ledger",
		[]string{CodeBundleUnlisted, CodeChainBroken, CodeSeqInvalid}, func(g vgen) {
			g.okLedger()
			b, _ := os.ReadFile(g.ledger())
			l := strings.SplitAfter(string(b), "\n")
			g.write("ledger.jsonl", []byte(l[0]+l[2]))
		}},
	{"ledger-entry-signature-invalid", "ledger", Critical, "the last entry's verdict was changed from OK to CRITICAL without signing it again",
		[]string{CodeEntrySignatureInvalid}, func(g vgen) {
			g.okLedger()
			b, _ := os.ReadFile(g.ledger())
			i := bytes.LastIndex(b, []byte(`\"verdict\":\"OK\"`))
			g.write("ledger.jsonl", append(append(b[:i:i], []byte(`\"verdict\":\"CRITICAL\"`)...), b[i+len(`\"verdict\":\"OK\"`):]...))
		}},
	{"ledger-anchor-mismatch", "ledger", Critical, "the genesis key is A, the trust anchor given is B", []string{CodeAnchorMismatch}, func(g vgen) {
		g.line(genesis(keyA, t0), keyA)
		g.write("bundles/.keep", nil)
		g.anchor(keyB)
	}},
	{"ledger-no-genesis", "ledger", Critical, "the first entry is a bundle entry, not the genesis key entry", []string{CodeAnchorMismatch}, func(g vgen) {
		g.line(bundleEntry(1, keyA, zeroHash, "b1", strings.Repeat("2", 64), t0), keyA)
		g.write("bundles/.keep", nil)
		g.anchor(keyA)
	}},
	{"ledger-old-key-after-rotation", "ledger", Critical, "after A handed over to B, a bundle and its entry are still signed by A",
		[]string{CodeEntrySignatureInvalid, CodeKeyMismatch, CodeSignerInvalid}, func(g vgen) {
			h := g.line(genesis(keyA, t0), keyA)
			h = g.line(rotation(2, keyA, keyB, h, t0), keyA)
			g.line(bundleEntry(3, keyA, h, "b1", g.bundle("bundles/b1", "b1", "OK", keyA, t0), t0), keyA)
			g.anchor(keyA)
		}},
	{"ledger-rotation-by-new-key", "ledger", Critical, "a rotation to B is signed by B instead of the current key A",
		[]string{CodeEntrySignatureInvalid, CodeSignerInvalid}, func(g vgen) {
			h := g.line(genesis(keyA, t0), keyA)
			g.line(rotation(2, keyB, keyB, h, t0), keyB)
			g.write("bundles/.keep", nil)
			g.anchor(keyA)
		}},
	{"ledger-key-entry-invalid", "ledger", Critical, "a rotation (signed by A) whose new_key_id is not the hash of its public_key",
		[]string{CodeKeyEntryInvalid}, func(g vgen) {
			h := g.line(genesis(keyA, t0), keyA)
			r := rotation(2, keyA, keyB, h, t0)
			r.NewKeyID = strings.Repeat("3", 64)
			g.line(r, keyA)
			g.write("bundles/.keep", nil)
			g.anchor(keyA)
		}},
	{"ledger-genesis-twice", "ledger", Critical, "a second genesis entry", []string{CodeKeyEntryInvalid}, func(g vgen) {
		h := g.line(genesis(keyA, t0), keyA)
		p := genesis(keyA, t0)
		p.Seq, p.Prev = 2, h
		g.line(p, keyA)
		g.write("bundles/.keep", nil)
		g.anchor(keyA)
	}},
	{"ledger-bundle-missing", "ledger", Critical, "the directory of bundle b2 was deleted", []string{CodeBundleMissing}, func(g vgen) {
		g.okLedger()
		g.must(os.RemoveAll(filepath.Join(g.dir, "bundles", "b2")))
	}},
	{"ledger-bundle-symlink", "ledger", Critical, "bundles/b2 is a symbolic link to the real bundle, kept outside the bundles directory",
		[]string{CodeBundleMissing}, func(g vgen) {
			g.okLedger()
			g.must(os.Rename(filepath.Join(g.dir, "bundles", "b2"), filepath.Join(g.dir, "b2.real")))
			g.must(os.Symlink("../b2.real", filepath.Join(g.dir, "bundles", "b2")))
		}},
	{"ledger-bundle-unlisted", "ledger", Critical, "a validly signed bundle b3 was added without an entry", []string{CodeBundleUnlisted}, func(g vgen) {
		g.okLedger()
		g.bundle("bundles/b3", "b3", "OK", keyA, t0.Add(2*time.Minute))
	}},
	{"ledger-bundle-duplicate", "ledger", Critical, "two entries name bundle b1", []string{CodeBundleDuplicate}, func(g vgen) {
		h := g.line(genesis(keyA, t0), keyA)
		sha := g.bundle("bundles/b1", "b1", "OK", keyA, t0)
		h = g.line(bundleEntry(2, keyA, h, "b1", sha, t0), keyA)
		g.line(bundleEntry(3, keyA, h, "b1", sha, t0), keyA)
		g.anchor(keyA)
	}},
	{"ledger-bundle-replaced", "ledger", Critical, "bundle b2 was replaced by another bundle b2, validly signed by A",
		[]string{CodeBundleHashMismatch}, func(g vgen) {
			g.okLedger()
			g.must(os.RemoveAll(filepath.Join(g.dir, "bundles", "b2")))
			g.bundle("bundles/b2", "b2", "OK", keyA, t0.Add(time.Hour))
		}},
	{"ledger-bundle-id-mismatch", "ledger", Critical, "directory b1 holds a bundle sealed as b9, and the entry recorded its manifest hash",
		[]string{CodeBundleIDMismatch}, func(g vgen) {
			h := g.line(genesis(keyA, t0), keyA)
			g.line(bundleEntry(2, keyA, h, "b1", g.bundle("bundles/b1", "b9", "OK", keyA, t0), t0), keyA)
			g.anchor(keyA)
		}},
	{"ledger-bundle-file-modified", "ledger", Critical, "one byte of a file in bundle b1 was changed", []string{CodeFileModified}, func(g vgen) {
		g.okLedger()
		g.replace("bundles/b1/serial.log", "login", "logon")
	}},

	// --- head statements (§6.8, §8.6) ---
	{"head-valid", "ledger", OK, "a head statement of the ledger's last entry", nil, func(g vgen) {
		g.okLedger()
		g.head("h1", keyA, t0.Add(2*time.Minute))
	}},
	{"head-valid-earlier", "ledger", OK, "head statements of entry 2 and of entry 3: the ledger grew after the first",
		nil, func(g vgen) {
			g.appendBundle("b1", keyA, t0)
			g.head("h1", keyA, t0.Add(30*time.Second))
			g.appendBundle("b2", keyA, t0.Add(time.Minute))
			g.head("h2", keyA, t0.Add(2*time.Minute))
			g.anchor(keyA)
		}},
	{"head-valid-rotation", "ledger", OK, "a head statement by A before a rotation to B, and one by B after it", nil, func(g vgen) {
		g.appendBundle("b1", keyA, t0)
		g.head("h1", keyA, t0.Add(30*time.Second))
		_, err := RotateAt(g.ledger(), keyA, pubOf(keyB), t0.Add(time.Minute))
		g.must(err)
		g.head("h2", keyB, t0.Add(90*time.Second))
		g.appendBundle("b2", keyB, t0.Add(2*time.Minute))
		g.anchor(keyA)
	}},
	{"head-ledger-truncated", "ledger", Critical, "the last entry and its bundle b2 were removed after a head statement of entry 3: without the statement the ledger verifies",
		[]string{CodeLedgerTruncated}, func(g vgen) {
			g.okLedger()
			g.head("h1", keyA, t0.Add(2*time.Minute))
			g.keepLines(2)
			g.must(os.RemoveAll(filepath.Join(g.dir, "bundles", "b2")))
		}},
	{"head-mismatch", "ledger", Critical, "after a head statement of entry 3, entry 3 and its bundle were replaced by another bundle, validly signed by A",
		[]string{CodeHeadMismatch}, func(g vgen) {
			g.okLedger()
			g.head("h1", keyA, t0.Add(2*time.Minute))
			g.keepLines(2)
			g.must(os.RemoveAll(filepath.Join(g.dir, "bundles", "b2")))
			g.appendBundle("b3", keyA, t0.Add(3*time.Minute))
		}},
	{"head-signature-invalid", "ledger", Critical, "the head statement's seq was changed from 3 to 2 without signing it again",
		[]string{CodeHeadSignatureInvalid}, func(g vgen) {
			g.okLedger()
			g.head("h1", keyA, t0.Add(2*time.Minute))
			g.replace("heads/h1.json", `\"seq\":3`, `\"seq\":2`)
		}},
	{"head-malformed", "ledger", Critical, "the head statement has no entry_sha256", []string{CodeHeadMalformed}, func(g vgen) {
		g.okLedger()
		g.headOf("h1", map[string]any{"format": HeadFormatV1, "format_version": 1, "time": "2026-11-16T12:02:00Z",
			"key_id": KeyID(pubOf(keyA)), "seq": 3}, keyA)
	}},
	{"head-unverifiable", "ledger", Unknown, "a head statement by B of entry 4; the ledger was cut back to before the rotation to B, so nothing it holds can check B's signature",
		[]string{CodeHeadUnverifiable}, func(g vgen) {
			g.appendBundle("b1", keyA, t0)
			_, err := RotateAt(g.ledger(), keyA, pubOf(keyB), t0.Add(time.Minute))
			g.must(err)
			g.appendBundle("b2", keyB, t0.Add(2*time.Minute))
			g.head("h1", keyB, t0.Add(3*time.Minute))
			g.keepLines(2)
			g.must(os.RemoveAll(filepath.Join(g.dir, "bundles", "b2")))
			g.anchor(keyA)
		}},
	{"head-wrong-key", "ledger", Critical, "a head statement of entry 2 signed by B: after entry 2 the current key was still A (the rotation to B is entry 3)",
		[]string{CodeHeadSignatureInvalid}, func(g vgen) {
			g.appendBundle("b1", keyA, t0)
			b, err := os.ReadFile(g.ledger())
			g.must(err)
			g.headOf("h1", map[string]any{"format": HeadFormatV1, "format_version": 1, "time": "2026-11-16T12:00:30Z",
				"key_id": KeyID(pubOf(keyB)), "seq": 2, "entry_sha256": lastHash(g.t, b)}, keyB)
			_, err = RotateAt(g.ledger(), keyA, pubOf(keyB), t0.Add(time.Minute))
			g.must(err)
			g.appendBundle("b2", keyB, t0.Add(2*time.Minute))
			g.anchor(keyA)
		}},
	{"head-old-key-beyond", "ledger", Critical, "a head statement of entry 5, beyond the ledger's 4, signed by A, which the rotation of entry 3 retired",
		[]string{CodeHeadSignatureInvalid}, func(g vgen) {
			g.appendBundle("b1", keyA, t0)
			_, err := RotateAt(g.ledger(), keyA, pubOf(keyB), t0.Add(time.Minute))
			g.must(err)
			g.appendBundle("b2", keyB, t0.Add(2*time.Minute))
			g.headOf("h1", map[string]any{"format": HeadFormatV1, "format_version": 1, "time": "2026-11-16T12:03:00Z",
				"key_id": KeyID(pubOf(keyA)), "seq": 5, "entry_sha256": strings.Repeat("ab", 32)}, keyA)
			g.anchor(keyA)
		}},
	{"head-ledger-missing", "ledger", Critical, "the ledger file is gone; a head statement by the trust anchor recorded entry 2",
		[]string{CodeLedgerMissing, CodeLedgerTruncated}, func(g vgen) {
			g.appendBundle("b1", keyA, t0)
			g.head("h1", keyA, t0.Add(time.Minute))
			g.must(os.Remove(g.ledger()))
			g.must(os.RemoveAll(filepath.Join(g.dir, "bundles", "b1")))
			g.write("bundles/.keep", nil)
			g.anchor(keyA)
		}},
	{"head-entry-malformed", "ledger", Critical, "a head statement of entry 3; then entry 3 was cut in half, so only 2 entries can be read",
		[]string{CodeBundleUnlisted, CodeEntryMalformed, CodeLedgerTruncated}, func(g vgen) {
			g.okLedger()
			g.head("h1", keyA, t0.Add(2*time.Minute))
			b, err := os.ReadFile(g.ledger())
			g.must(err)
			g.write("ledger.jsonl", b[:len(b)-40])
		}},
	{"head-reformatted", "ledger", OK, "a valid head statement re-encoded with indentation and CRLF line ends: only the payload's value is signed",
		nil, func(g vgen) {
			g.okLedger()
			g.head("h1", keyA, t0.Add(2*time.Minute))
			b, err := os.ReadFile(filepath.Join(g.dir, "heads", "h1.json"))
			g.must(err)
			var w line
			g.must(json.Unmarshal(b, &w))
			out, err := json.MarshalIndent(map[string]string{"sig": w.Sig, "payload": w.Payload}, "", "  ")
			g.must(err)
			g.write("heads/h1.json", []byte(strings.ReplaceAll(string(out), "\n", "\r\n")+"\r\n"))
		}},
	{"head-one-bad-one-good", "ledger", Critical, "two head statements: the first is not JSON, the second is valid and is still checked",
		[]string{CodeHeadMalformed}, func(g vgen) {
			g.okLedger()
			g.write("heads/h1.json", []byte("ledger head: seq 3\n"))
			g.head("h2", keyA, t0.Add(2*time.Minute))
		}},
	{"head-after-unsupported-entry", "ledger", Unknown, "a head statement of entry 2; entry 2 has format_version 2, which a v1 verifier cannot read: that is not a cut",
		[]string{CodeFormatUnsupported, CodeHeadUnverifiable}, func(g vgen) {
			h := g.line(genesis(keyA, t0), keyA)
			r := rotation(2, keyA, keyB, h, t0)
			r.FormatVersion = 2
			h2 := g.line(r, keyA)
			g.headOf("h1", map[string]any{"format": HeadFormatV1, "format_version": 1, "time": "2026-11-16T12:01:00Z",
				"key_id": KeyID(pubOf(keyA)), "seq": 2, "entry_sha256": h2}, keyA)
			g.write("bundles/.keep", nil)
			g.anchor(keyA)
		}},
	{"head-format-unsupported", "ledger", Unknown, "a head statement with format_version 2 (validly signed)", []string{CodeFormatUnsupported}, func(g vgen) {
		g.okLedger()
		b, err := os.ReadFile(g.ledger())
		g.must(err)
		g.headOf("h1", map[string]any{"format": HeadFormatV1, "format_version": 2, "time": "2026-11-16T12:02:00Z",
			"key_id": KeyID(pubOf(keyA)), "seq": 3, "entry_sha256": lastHash(g.t, b)}, keyA)
	}},
}

// lastHash is the entry hash of a ledger's last line.
func lastHash(t *testing.T, ledger []byte) string {
	l := strings.Split(strings.TrimSuffix(string(ledger), "\n"), "\n")
	var w line
	if err := json.Unmarshal([]byte(l[len(l)-1]), &w); err != nil {
		t.Fatal(err)
	}
	return sha256Hex([]byte(w.Payload))
}

func runVector(dir string) (string, []string, error) {
	var exp Expected
	b, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		return "", nil, err
	}
	var anchor ed25519.PublicKey
	if _, err := os.Stat(filepath.Join(dir, "anchor.pub")); err == nil {
		if anchor, err = LoadPublicKey(filepath.Join(dir, "anchor.pub")); err != nil {
			return "", nil, err
		}
	}
	if exp.Mode == "bundle" {
		r := VerifyBundle(filepath.Join(dir, "bundle"), anchor)
		return r.Verdict, codes(r.Findings), nil
	}
	var heads [][]byte
	names, _ := filepath.Glob(filepath.Join(dir, "heads", "*.json"))
	slices.Sort(names)
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			return "", nil, err
		}
		heads = append(heads, b)
	}
	r, err := VerifyLedgerHeads(filepath.Join(dir, "ledger.jsonl"), filepath.Join(dir, "bundles"), anchor, heads)
	return r.Verdict, codes(r.AllFindings()), err
}

func TestVectors(t *testing.T) {
	gen := t.TempDir()
	for _, v := range vectors {
		g := vgen{t: t, dir: filepath.Join(gen, v.name)}
		g.must(os.MkdirAll(g.dir, 0o755))
		v.make(g)
		f := slices.Clone(v.findings)
		slices.Sort(f)
		if f == nil {
			f = []string{}
		}
		b, err := json.MarshalIndent(Expected{Description: v.description, Mode: v.mode, Verdict: v.verdict, Findings: f}, "", "  ")
		g.must(err)
		g.write("expected.json", append(b, '\n'))
	}
	if *update {
		names := map[string]bool{}
		for _, v := range vectors {
			names[v.name] = true
			if err := os.RemoveAll(filepath.Join(vectorsDir, v.name)); err != nil {
				t.Fatal(err)
			}
			if err := os.CopyFS(filepath.Join(vectorsDir, v.name), os.DirFS(filepath.Join(gen, v.name))); err != nil {
				t.Fatal(err)
			}
		}
		des, _ := os.ReadDir(vectorsDir)
		for _, d := range des {
			if d.IsDir() && !names[d.Name()] {
				t.Fatalf("%s: a vector directory the generator does not make", d.Name())
			}
		}
	}
	// The committed vectors are byte for byte what the generator makes.
	for _, v := range vectors {
		want, got := treeOf(t, filepath.Join(gen, v.name)), treeOf(t, filepath.Join(vectorsDir, v.name))
		if !maps(want, got) {
			t.Errorf("%s: the committed vector differs from the generator's (run with -update)", v.name)
		}
	}
	// Every vector gives exactly its declared verdict and findings.
	for _, v := range vectors {
		verdict, got, err := runVector(filepath.Join(vectorsDir, v.name))
		want := slices.Clone(v.findings)
		slices.Sort(want)
		if err != nil || verdict != v.verdict || !slices.Equal(got, want) {
			t.Errorf("%s: verdict %s findings %v (err %v), declared %s %v", v.name, verdict, got, err, v.verdict, want)
		}
	}
}

// treeOf maps every path under dir to its content (a symlink to "->target").
func treeOf(t *testing.T, dir string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			out[rel] = "->" + target
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel] = string(b)
		return nil
	})
	return out
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
