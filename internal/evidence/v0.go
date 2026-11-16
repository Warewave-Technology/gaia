package evidence

// v0: the format Gaia wrote before the specification (T7–T11, manifests "gaia-evidence/v1" and ledger
// entries "gaia-ledger/v1"). It is verified here exactly as it was, and never written again; see the
// specification's appendix A. Its ledger entry hash depends on Go's encoding/json, which is why v1 replaced it.

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	formatV0       = "gaia-evidence/v1"
	ledgerFormatV0 = "gaia-ledger/v1"
	zeroHash       = "0000000000000000000000000000000000000000000000000000000000000000"
)

type manifestV0 struct {
	Format         string            `json:"format"`
	BundleID       string            `json:"bundle_id"`
	Created        time.Time         `json:"created"`
	Subject        map[string]string `json:"subject"`
	KeyFingerprint string            `json:"key_fingerprint"`
	Files          []File            `json:"files"`
}

// verifyBundleV0 checks a v0 bundle; every problem is a v0_integrity finding.
func verifyBundleV0(dir string, mb []byte, pub ed25519.PublicKey) BundleReport {
	r := newBundleReport(dir, "v0")
	r.SHA256 = sha256Hex(mb)
	bad := func(f string, a ...any) { r.add(CodeV0Integrity, "", fmt.Sprintf(f, a...)) }
	if pub == nil {
		r.add(CodeAnchorMissing, "", "no public key given: the signature cannot be checked")
	} else if sb, err := os.ReadFile(filepath.Join(dir, SignatureFile)); err != nil {
		bad("signature: %v", err)
	} else if sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sb))); err != nil {
		bad("signature: %v", err)
	} else if !ed25519.Verify(pub, mb, sig) {
		bad("signature does not match manifest.json for key %s", KeyID(pub))
	}
	var m manifestV0
	if err := json.Unmarshal(mb, &m); err != nil {
		bad("manifest: %v", err)
		return r.done()
	}
	r.BundleID, r.KeyID = m.BundleID, m.KeyFingerprint
	actual, err := hashTreeV0(dir)
	if err != nil {
		bad("reading files: %v", err)
		return r.done()
	}
	r.Files = len(actual)
	have := map[string]File{}
	for _, f := range actual {
		have[f.Path] = f
	}
	for _, f := range m.Files {
		a, ok := have[f.Path]
		switch {
		case !ok:
			bad("%s: listed but missing", f.Path)
		case a.SHA256 != f.SHA256 || a.Bytes != f.Bytes:
			bad("%s: sha256 %s (%d bytes), manifest says %s (%d bytes)", f.Path, a.SHA256, a.Bytes, f.SHA256, f.Bytes)
		}
		delete(have, f.Path)
	}
	for _, p := range slices.Sorted(mapKeys(have)) {
		bad("%s: not in the manifest", p)
	}
	return r.done()
}

// hashTreeV0 hashes the regular files under dir, except the seal files, in path order (v0's rule: anything
// that is not a regular file is an error).
func hashTreeV0(dir string) ([]File, error) {
	files, odd, err := walkBundle(dir)
	if err != nil {
		return nil, err
	}
	if len(odd) > 0 {
		return nil, fmt.Errorf("%s: not a regular file", odd[0])
	}
	return files, nil
}

// entryV0 is one v0 ledger line. EntrySHA256 is the sha256 of the line's JSON (Go's encoding/json) with
// EntrySHA256 and Sig empty; Sig is the ed25519 signature of EntrySHA256's hex string.
type entryV0 struct {
	Format       string    `json:"format"`
	Seq          int       `json:"seq"`
	Time         time.Time `json:"time"`
	BundleID     string    `json:"bundle_id"`
	BundleSHA256 string    `json:"bundle_sha256"`
	Subject      string    `json:"subject"`
	Verdict      string    `json:"verdict"`
	Prev         string    `json:"prev"`
	EntrySHA256  string    `json:"entry_sha256,omitempty"`
	Sig          string    `json:"sig,omitempty"`
}

func (e entryV0) hash() string {
	e.EntrySHA256, e.Sig = "", ""
	b, _ := json.Marshal(e)
	return sha256Hex(b)
}

// readLedgerV0 parses and checks the chain and, with pub set, every signature.
func readLedgerV0(r io.Reader, pub ed25519.PublicKey) ([]entryV0, []string) {
	var (
		out      []entryV0
		problems []string
	)
	prev := zeroHash
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for line := 1; sc.Scan(); line++ {
		raw := sc.Bytes()
		var e entryV0
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		err := dec.Decode(&e)
		if err == nil && dec.Decode(&struct{}{}) != io.EOF {
			err = errors.New("data after the entry")
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d: %v", line, err))
			prev = "" // the chain is broken from here on
			continue
		}
		switch {
		case e.Format != ledgerFormatV0:
			problems = append(problems, fmt.Sprintf("line %d: format %q", line, e.Format))
		case e.Seq != line:
			problems = append(problems, fmt.Sprintf("line %d: seq %d", line, e.Seq))
		}
		if e.Prev != prev {
			problems = append(problems, fmt.Sprintf("line %d: prev %s does not match the entry before (%s)", line, e.Prev, orNone(prev)))
		}
		if h := e.hash(); h != e.EntrySHA256 {
			problems = append(problems, fmt.Sprintf("line %d: entry hash %s, recorded %s", line, h, e.EntrySHA256))
		}
		if pub != nil {
			sig, err := base64.StdEncoding.DecodeString(e.Sig)
			if err != nil || !ed25519.Verify(pub, []byte(e.EntrySHA256), sig) {
				problems = append(problems, fmt.Sprintf("line %d: signature invalid for key %s", line, KeyID(pub)))
			}
		}
		out = append(out, e)
		prev = e.EntrySHA256
	}
	if err := sc.Err(); err != nil {
		problems = append(problems, err.Error())
	}
	return out, problems
}

func orNone(s string) string {
	if s == "" {
		return "unreadable"
	}
	return s
}

// verifyLedgerV0 is v0's ledger check: the chain, every signature, every listed bundle against its recorded
// manifest hash, and no bundle directory without an entry.
func verifyLedgerV0(r *LedgerReport, data []byte, bundlesDir string, pub ed25519.PublicKey) {
	r.Format = "v0"
	if pub == nil {
		r.add(CodeAnchorMissing, "", "no public key given: signatures cannot be checked")
	} else {
		r.Keys = []string{KeyID(pub)}
	}
	entries, problems := readLedgerV0(bytes.NewReader(data), pub)
	r.Entries = len(entries)
	for _, p := range problems {
		r.add(CodeV0Integrity, "", p)
	}
	if len(entries) == 0 && len(problems) == 0 {
		r.add(CodeLedgerEmpty, "", "the ledger has no entries")
	}
	listed := map[string]bool{}
	for _, e := range entries {
		if !validBundleID(e.BundleID) {
			r.add(CodeV0Integrity, fmt.Sprintf("seq %d", e.Seq), fmt.Sprintf("bad bundle id %q", e.BundleID))
			continue
		}
		listed[e.BundleID] = true
		dir := filepath.Join(bundlesDir, e.BundleID)
		mb, err := os.ReadFile(filepath.Join(dir, ManifestFile))
		if err != nil {
			br := newBundleReport(dir, "v0")
			br.BundleID = e.BundleID
			br.add(CodeV0Integrity, "", fmt.Sprintf("manifest: %v", err))
			r.Bundles = append(r.Bundles, br.done())
			continue
		}
		br := verifyBundleV0(dir, mb, pub)
		if br.SHA256 != e.BundleSHA256 {
			br.add(CodeV0Integrity, "", fmt.Sprintf("manifest sha256 %s, ledger seq %d recorded %s", br.SHA256, e.Seq, e.BundleSHA256))
		}
		if br.BundleID != "" && br.BundleID != e.BundleID {
			br.add(CodeV0Integrity, "", fmt.Sprintf("manifest names bundle %q, ledger seq %d %q", br.BundleID, e.Seq, e.BundleID))
		}
		r.Bundles = append(r.Bundles, br.done())
	}
	r.unlisted(bundlesDir, listed)
}
