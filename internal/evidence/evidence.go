// Package evidence seals verification artefacts (ADR-002 #3): every file of a bundle
// is sha256-hashed into manifest.json, the manifest is signed with ed25519
// (manifest.sig), and the manifest's hash is appended to an append-only ledger in
// which every entry carries the hash of the previous one and its own signature.
// Changing one byte of an artefact, a manifest, a signature or a ledger line is
// detected by Verify*: the hashes no longer match, and without the private key the
// signatures cannot be redone.
package evidence

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Names of the files a bundle's seal consists of.
const (
	ManifestFile  = "manifest.json"
	SignatureFile = "manifest.sig"
	formatV1      = "gaia-evidence/v1"
	ledgerFormat  = "gaia-ledger/v1"
	zeroHash      = "0000000000000000000000000000000000000000000000000000000000000000"
)

// --- keys ---

// GenerateKey writes a new ed25519 key pair: the private key as PKCS#8 PEM (mode 0600), the public key as PKIX PEM.
func GenerateKey(privPath, pubPath string) (fingerprint string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	if err := writeNew(privPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), 0o600); err != nil {
		return "", err
	}
	if err := writeNew(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o644); err != nil {
		return "", err
	}
	return Fingerprint(pub), nil
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Fingerprint is the hex sha256 of the raw public key.
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])
}

// LoadPrivateKey reads a PKCS#8 PEM ed25519 private key.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	der, err := readPEM(path, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ed25519 key", path)
	}
	return priv, nil
}

// LoadPublicKey reads a PKIX PEM ed25519 public key.
func LoadPublicKey(path string) (ed25519.PublicKey, error) {
	der, err := readPEM(path, "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an ed25519 key", path)
	}
	return pub, nil
}

func readPEM(path, typ string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != typ {
		return nil, fmt.Errorf("%s: no %s PEM block", path, typ)
	}
	return blk.Bytes, nil
}

// --- bundles ---

// File is one artefact in a manifest.
type File struct {
	Path   string `json:"path"` // relative, slash-separated
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Manifest lists a bundle's artefacts.
type Manifest struct {
	Format         string            `json:"format"`
	BundleID       string            `json:"bundle_id"`
	Created        time.Time         `json:"created"`
	Subject        map[string]string `json:"subject"` // e.g. vm, verdict
	KeyFingerprint string            `json:"key_fingerprint"`
	Files          []File            `json:"files"`
}

// Seal hashes every file under dir into manifest.json and signs it. It returns the
// bundle hash: the sha256 of manifest.json, which is what goes into the ledger.
func Seal(dir, bundleID string, subject map[string]string, key ed25519.PrivateKey) (string, error) {
	files, err := hashTree(dir)
	if err != nil {
		return "", err
	}
	m := Manifest{Format: formatV1, BundleID: bundleID, Created: time.Now().UTC(), Subject: subject,
		KeyFingerprint: Fingerprint(key.Public().(ed25519.PublicKey)), Files: files}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	if err := writeNew(filepath.Join(dir, ManifestFile), b, 0o644); err != nil {
		return "", err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, b)) + "\n"
	if err := writeNew(filepath.Join(dir, SignatureFile), []byte(sig), 0o644); err != nil {
		return "", err
	}
	return sha256Hex(b), nil
}

// hashTree hashes the regular files under dir, except the seal files, in path order.
func hashTree(dir string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() || rel == ManifestFile || rel == SignatureFile {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", rel)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return err
		}
		out = append(out, File{Path: rel, SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n})
		return nil
	})
	slices.SortFunc(out, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	return out, err
}

// BundleReport is the outcome of checking one bundle.
type BundleReport struct {
	Dir      string   `json:"dir"`
	BundleID string   `json:"bundle_id,omitempty"`
	SHA256   string   `json:"bundle_sha256,omitempty"` // sha256 of manifest.json as found
	Files    int      `json:"files"`
	Problems []string `json:"problems"` // empty = intact
}

// VerifyBundle checks the signature of manifest.json and every listed file, and that no file was added or removed.
func VerifyBundle(dir string, pub ed25519.PublicKey) BundleReport {
	r := BundleReport{Dir: dir, Problems: []string{}}
	bad := func(f string, a ...any) { r.Problems = append(r.Problems, fmt.Sprintf(f, a...)) }
	mb, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		bad("manifest: %v", err)
		return r
	}
	r.SHA256 = sha256Hex(mb)
	sb, err := os.ReadFile(filepath.Join(dir, SignatureFile))
	if err != nil {
		bad("signature: %v", err)
	} else if sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sb))); err != nil {
		bad("signature: %v", err)
	} else if !ed25519.Verify(pub, mb, sig) {
		bad("signature does not match manifest.json for key %s", Fingerprint(pub))
	}
	var m Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		bad("manifest: %v", err)
		return r
	}
	r.BundleID = m.BundleID
	if m.Format != formatV1 {
		bad("manifest format %q", m.Format)
	}
	actual, err := hashTree(dir)
	if err != nil {
		bad("reading files: %v", err)
		return r
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
	for p := range have {
		bad("%s: not in the manifest", p)
	}
	slices.Sort(r.Problems)
	return r
}

// --- ledger ---

// Entry is one ledger line. EntrySHA256 is the sha256 of the line's JSON with EntrySHA256
// and Sig empty; Sig is the ed25519 signature of EntrySHA256's hex string.
type Entry struct {
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

func (e Entry) hash() string {
	e.EntrySHA256, e.Sig = "", ""
	b, _ := json.Marshal(e)
	return sha256Hex(b)
}

// Append adds an entry for a sealed bundle to the ledger file (created if missing), under an exclusive lock.
func Append(ledger string, e Entry, key ed25519.PrivateKey) (Entry, error) {
	f, err := os.OpenFile(ledger, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return e, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return e, err
	}
	entries, problems := readLedger(f, nil)
	if len(problems) > 0 {
		return e, fmt.Errorf("ledger %s is not intact, not appending: %s", ledger, strings.Join(problems, "; "))
	}
	e.Format, e.Seq, e.Prev = ledgerFormat, len(entries)+1, zeroHash
	if len(entries) > 0 {
		e.Prev = entries[len(entries)-1].EntrySHA256
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.EntrySHA256 = e.hash()
	e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(e.EntrySHA256)))
	b, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return e, err
	}
	return e, f.Sync()
}

// readLedger parses and checks the chain; with pub set it also checks every signature.
func readLedger(r io.Reader, pub ed25519.PublicKey) ([]Entry, []string) {
	var (
		out      []Entry
		problems []string
	)
	prev := zeroHash
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for line := 1; sc.Scan(); line++ {
		raw := sc.Bytes()
		var e Entry
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
		case e.Format != ledgerFormat:
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
				problems = append(problems, fmt.Sprintf("line %d: signature invalid for key %s", line, Fingerprint(pub)))
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

// LedgerReport is the outcome of checking a ledger and the bundles it lists.
type LedgerReport struct {
	Ledger   string         `json:"ledger"`
	Entries  int            `json:"entries"`
	Problems []string       `json:"problems"` // ledger-level problems
	Bundles  []BundleReport `json:"bundles"`
	Intact   bool           `json:"intact"`
}

// VerifyLedger checks the hash chain and signatures of the ledger and, for every entry,
// that the bundle in bundlesDir/<bundle_id> is intact and is the one the entry recorded,
// and that every bundle directory in bundlesDir has an entry. (A ledger cut off together
// with its newest bundles still verifies: that needs an anchor outside, e.g. RFC 3161.)
func VerifyLedger(ledger, bundlesDir string, pub ed25519.PublicKey) (LedgerReport, error) {
	r := LedgerReport{Ledger: ledger, Problems: []string{}, Bundles: []BundleReport{}}
	f, err := os.Open(ledger)
	if err != nil {
		return r, err
	}
	defer f.Close()
	entries, problems := readLedger(f, pub)
	r.Entries, r.Problems = len(entries), append(r.Problems, problems...)
	if len(entries) == 0 && len(problems) == 0 {
		r.Problems = append(r.Problems, "ledger is empty")
	}
	for _, e := range entries {
		if e.BundleID == "" || strings.ContainsAny(e.BundleID, `/\`) || e.BundleID == "." || e.BundleID == ".." {
			r.Problems = append(r.Problems, fmt.Sprintf("seq %d: bad bundle id %q", e.Seq, e.BundleID))
			continue
		}
		br := VerifyBundle(filepath.Join(bundlesDir, e.BundleID), pub)
		if br.SHA256 != "" && br.SHA256 != e.BundleSHA256 {
			br.Problems = append(br.Problems, fmt.Sprintf("manifest sha256 %s, ledger seq %d recorded %s", br.SHA256, e.Seq, e.BundleSHA256))
		}
		if br.BundleID != "" && br.BundleID != e.BundleID {
			br.Problems = append(br.Problems, fmt.Sprintf("manifest names bundle %q, ledger seq %d %q", br.BundleID, e.Seq, e.BundleID))
		}
		r.Bundles = append(r.Bundles, br)
	}
	// A bundle without a ledger entry: the ledger lost its tail, or the bundle was slipped in.
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.BundleID] = true
	}
	if des, err := os.ReadDir(bundlesDir); err != nil {
		r.Problems = append(r.Problems, fmt.Sprintf("bundles directory: %v", err))
	} else {
		for _, d := range des {
			if d.IsDir() && !listed[d.Name()] {
				r.Problems = append(r.Problems, fmt.Sprintf("bundle %s is not in the ledger", d.Name()))
			}
		}
	}
	r.Intact = len(r.Problems) == 0
	for _, b := range r.Bundles {
		r.Intact = r.Intact && len(b.Problems) == 0
	}
	return r, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
