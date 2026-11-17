// Package evidence implements the Restore Evidence Format v1 (docs/spec/evidence-v1.md, ADR-006):
// bundles whose files are sha256-hashed into a manifest signed with ed25519, and an append-only ledger in
// which every entry is signed and carries the hash of the one before. The ledger also carries the signing
// keys: its first entry declares the key a verifier must already trust (the trust anchor), and a rotation
// entry, signed by the current key, hands over to a new one. Bundles and ledgers written before the
// specification (v0) are still verified, read-only (v0.go).
package evidence

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Names and identifiers from the specification.
const (
	ManifestFile   = "manifest.json"
	SignatureFile  = "manifest.sig"
	FormatV1       = "restore-evidence"
	LedgerFormatV1 = "restore-evidence-ledger"
	FormatVersion  = 1
)

// Finding codes (specification §8.3).
const (
	CodeManifestMissing       = "manifest_missing"
	CodeManifestMalformed     = "manifest_malformed"
	CodeFormatUnsupported     = "format_unsupported"
	CodeKeyMismatch           = "key_mismatch"
	CodeSignatureMissing      = "signature_missing"
	CodeSignatureMalformed    = "signature_malformed"
	CodeSignatureInvalid      = "signature_invalid"
	CodePathInvalid           = "path_invalid"
	CodeFileMissing           = "file_missing"
	CodeFileModified          = "file_modified"
	CodeFileUnlisted          = "file_unlisted"
	CodeFileTypeInvalid       = "file_type_invalid"
	CodeAnchorMissing         = "anchor_missing"
	CodeLedgerMissing         = "ledger_missing"
	CodeLedgerEmpty           = "ledger_empty"
	CodeEntryMalformed        = "entry_malformed"
	CodeSeqInvalid            = "seq_invalid"
	CodeChainBroken           = "chain_broken"
	CodeEntrySignatureInvalid = "entry_signature_invalid"
	CodeAnchorMismatch        = "anchor_mismatch"
	CodeSignerInvalid         = "signer_invalid"
	CodeKeyEntryInvalid       = "key_entry_invalid"
	CodeBundleMissing         = "bundle_missing"
	CodeBundleUnlisted        = "bundle_unlisted"
	CodeBundleDuplicate       = "bundle_duplicate"
	CodeBundleHashMismatch    = "bundle_hash_mismatch"
	CodeBundleIDMismatch      = "bundle_id_mismatch"
	CodeV0Integrity           = "v0_integrity" // v0 only: the free-text problems of Gaia's pre-specification check
)

// Verdicts of a verification.
const (
	OK       = "OK"
	Critical = "CRITICAL"
	Unknown  = "UNKNOWN"
)

// Severity of a finding: what was not checked is UNKNOWN; everything else is a failed check.
func Severity(code string) string {
	if code == CodeFormatUnsupported || code == CodeAnchorMissing || code == CodeHeadUnverifiable {
		return Unknown
	}
	return Critical
}

// Finding is one result of a verification that is not OK.
type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Where    string `json:"where,omitempty"` // e.g. "line 3", a file path
	Detail   string `json:"detail"`
}

// Verdict combines findings: CRITICAL if any is, else UNKNOWN if any is, else OK.
func Verdict(fs []Finding) string {
	v := OK
	for _, f := range fs {
		switch f.Severity {
		case Critical:
			return Critical
		case Unknown:
			v = Unknown
		}
	}
	return v
}

// --- reports ---

// BundleReport is the outcome of checking one bundle.
type BundleReport struct {
	Dir      string    `json:"dir"`
	Format   string    `json:"format"` // v1, v0, or "" when the manifest is unreadable
	BundleID string    `json:"bundle_id,omitempty"`
	SHA256   string    `json:"bundle_sha256,omitempty"` // sha256 of manifest.json as found
	KeyID    string    `json:"key_id,omitempty"`
	Files    int       `json:"files"`
	Verdict  string    `json:"verdict"`
	Findings []Finding `json:"findings"`
	Problems []string  `json:"problems"` // the findings as text (empty = intact)
}

func newBundleReport(dir, format string) *BundleReport {
	return &BundleReport{Dir: dir, Format: format, Findings: []Finding{}, Problems: []string{}}
}

func (r *BundleReport) add(code, where, detail string) {
	r.Findings = append(r.Findings, Finding{Code: code, Severity: Severity(code), Where: where, Detail: detail})
}

func (r *BundleReport) done() BundleReport {
	r.Verdict = Verdict(r.Findings)
	r.Problems = findingText(r.Findings)
	return *r
}

// LedgerReport is the outcome of checking a ledger and the bundles it lists.
type LedgerReport struct {
	Ledger   string         `json:"ledger"`
	Format   string         `json:"format"`
	Entries  int            `json:"entries"`
	Keys     []string       `json:"keys"` // key_ids in the order they became current
	Verdict  string         `json:"verdict"`
	Findings []Finding      `json:"findings"` // ledger-level
	Problems []string       `json:"problems"` // the ledger-level findings as text
	Bundles  []BundleReport `json:"bundles"`
	Heads    []HeadCheck    `json:"heads"`  // the head statements given, in order (head.go)
	Intact   bool           `json:"intact"` // verdict OK
}

func (r *LedgerReport) add(code, where, detail string) {
	r.Findings = append(r.Findings, Finding{Code: code, Severity: Severity(code), Where: where, Detail: detail})
}

// unlisted reports every directory in bundlesDir that no entry names.
func (r *LedgerReport) unlisted(bundlesDir string, listed map[string]bool) {
	des, err := os.ReadDir(bundlesDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.add(CodeBundleMissing, bundlesDir, fmt.Sprintf("bundles directory: %v", err))
		return
	}
	for _, d := range des {
		if d.IsDir() && !listed[d.Name()] {
			r.add(CodeBundleUnlisted, d.Name(), "a bundle directory that no ledger entry names")
		}
	}
}

func (r *LedgerReport) done() LedgerReport {
	all := slices.Clone(r.Findings)
	for _, b := range r.Bundles {
		all = append(all, b.Findings...)
	}
	r.Verdict = Verdict(all)
	r.Intact = r.Verdict == OK
	r.Problems = findingText(r.Findings)
	if r.Keys == nil {
		r.Keys = []string{}
	}
	if r.Heads == nil {
		r.Heads = []HeadCheck{}
	}
	return *r
}

// AllFindings returns the ledger's findings and those of its bundles.
func (r LedgerReport) AllFindings() []Finding {
	all := slices.Clone(r.Findings)
	for _, b := range r.Bundles {
		for _, f := range b.Findings {
			if f.Where == "" {
				f.Where = b.BundleID
			} else {
				f.Where = b.BundleID + "/" + f.Where
			}
			all = append(all, f)
		}
	}
	return all
}

func findingText(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		s := f.Code + ": " + f.Detail
		if f.Where != "" {
			s = f.Where + ": " + s
		}
		out = append(out, s)
	}
	return out
}

// --- syntax ---

var (
	hex64Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	timeRe     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)
	bundleIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)
	intRe      = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})$`)
)

func validTime(s string) bool {
	if !timeRe.MatchString(s) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, s)
	return err == nil
}

func validBundleID(s string) bool { return bundleIDRe.MatchString(s) && s != "." && s != ".." }

// validPath is the specification's rule for a manifest path (§5.3).
func validPath(p string) bool {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00") || p == ManifestFile || p == SignatureFile {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// parseObject parses b as exactly one JSON object with no duplicate member names at any depth;
// numbers stay json.Number.
func parseObject(b []byte) (map[string]any, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("not UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := noDuplicates(dec); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("data after the JSON value")
	}
	var m map[string]any
	dec = json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// noDuplicates reads one JSON value from dec and fails on a repeated member name in any object.
func noDuplicates(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return err
			}
			name := k.(string)
			if seen[name] {
				return fmt.Errorf("member %q appears twice", name)
			}
			seen[name] = true
			if err := noDuplicates(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := noDuplicates(dec); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}

// field helpers: a missing member or one of the wrong type is an error naming it.
func str(m map[string]any, k string) (string, error) {
	v, ok := m[k].(string)
	if !ok {
		return "", fmt.Errorf("%q: missing or not a string", k)
	}
	return v, nil
}

func integer(m map[string]any, k string) (int64, error) {
	n, ok := m[k].(json.Number)
	if !ok || !intRe.MatchString(string(n)) {
		return 0, fmt.Errorf("%q: missing or not a non-negative integer", k)
	}
	v, err := n.Int64()
	return v, err
}

func hex64(m map[string]any, k string) (string, error) {
	v, err := str(m, k)
	if err == nil && !hex64Re.MatchString(v) {
		err = fmt.Errorf("%q: not 64 lowercase hex digits", k)
	}
	return v, err
}

// --- bundles ---

// File is one artefact in a manifest.
type File struct {
	Path   string `json:"path"` // relative, slash-separated
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Manifest is manifest.json of a v1 bundle.
type Manifest struct {
	Format        string            `json:"format"`
	FormatVersion int               `json:"format_version"`
	BundleID      string            `json:"bundle_id"`
	Created       string            `json:"created"`
	KeyID         string            `json:"key_id"`
	Subject       map[string]string `json:"subject"`
	Files         []File            `json:"files"`
}

// Seal hashes every file under dir into manifest.json and signs it (manifest.sig). It returns the bundle
// hash: the sha256 of manifest.json, which goes into the ledger.
func Seal(dir, bundleID string, subject map[string]string, key ed25519.PrivateKey) (string, error) {
	return SealAt(dir, bundleID, subject, key, time.Now())
}

// SealAt is Seal with a given creation time (for reproducible test vectors).
func SealAt(dir, bundleID string, subject map[string]string, key ed25519.PrivateKey, created time.Time) (string, error) {
	if !validBundleID(bundleID) {
		return "", fmt.Errorf("bundle id %q: want 1-255 of A-Z a-z 0-9 . _ -", bundleID)
	}
	files, odd, err := walkBundle(dir)
	if err != nil {
		return "", err
	}
	if len(odd) > 0 {
		return "", fmt.Errorf("%s: not a regular file", odd[0])
	}
	for _, f := range files {
		if !validPath(f.Path) {
			return "", fmt.Errorf("%q: not a valid bundle path", f.Path)
		}
	}
	if subject == nil {
		subject = map[string]string{}
	}
	m := Manifest{Format: FormatV1, FormatVersion: FormatVersion, BundleID: bundleID, Created: created.UTC().Format(time.RFC3339Nano),
		KeyID: KeyID(key.Public().(ed25519.PublicKey)), Subject: subject, Files: files}
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

// walkBundle hashes the regular files under dir, except the seal files at its root, sorted by path (byte
// order); odd lists everything else that is not a directory (symlinks, devices, ...).
func walkBundle(dir string) (files []File, odd []string, err error) {
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case p == dir || rel == ManifestFile || rel == SignatureFile:
			return nil
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			odd = append(odd, rel)
			return nil
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
		files = append(files, File{Path: rel, SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n})
		return nil
	})
	slices.SortFunc(files, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(odd)
	return files, odd, err
}

// VerifyBundle checks one bundle against the key that should have signed it (nil: no trust anchor, and
// the signature is not checked).
func VerifyBundle(dir string, pub ed25519.PublicKey) BundleReport {
	return verifyBundle(dir, pub)
}

func verifyBundle(dir string, key ed25519.PublicKey) BundleReport {
	mb, err := readRegular(filepath.Join(dir, ManifestFile))
	if err != nil {
		r := newBundleReport(dir, "")
		r.add(CodeManifestMissing, ManifestFile, err.Error())
		return r.done()
	}
	m, err := parseObject(mb)
	if err != nil {
		r := newBundleReport(dir, "")
		r.SHA256 = sha256Hex(mb)
		r.add(CodeManifestMalformed, ManifestFile, err.Error())
		return r.done()
	}
	if f, _ := m["format"].(string); f == formatV0 {
		return verifyBundleV0(dir, mb, key)
	}
	r := newBundleReport(dir, "v1")
	r.SHA256 = sha256Hex(mb)
	man, code, err := parseManifest(m)
	if err != nil {
		r.add(code, ManifestFile, err.Error())
		return r.done()
	}
	r.BundleID, r.KeyID = man.BundleID, man.KeyID

	// The signature, by the key that should have signed this bundle.
	switch {
	case key == nil:
		r.add(CodeAnchorMissing, "", "no public key given: the signature is not checked")
	case man.KeyID != KeyID(key):
		r.add(CodeKeyMismatch, ManifestFile, fmt.Sprintf("signed by key %s, expected %s", man.KeyID, KeyID(key)))
	default:
		sb, err := readRegular(filepath.Join(dir, SignatureFile))
		if err != nil {
			r.add(CodeSignatureMissing, SignatureFile, err.Error())
			break
		}
		sig, ok := decodeSignature(sb)
		if !ok {
			r.add(CodeSignatureMalformed, SignatureFile, "not the base64 of 64 bytes, optionally followed by one line feed")
		} else if !ed25519.Verify(key, mb, sig) {
			r.add(CodeSignatureInvalid, SignatureFile, "the signature does not match manifest.json's bytes")
		}
	}

	// The files.
	listed := map[string]File{}
	prev := ""
	for i, f := range man.Files {
		switch {
		case !validPath(f.Path):
			r.add(CodePathInvalid, f.Path, "not a valid bundle path")
			continue
		case i > 0 && f.Path <= prev:
			r.add(CodePathInvalid, f.Path, "files are not in strictly ascending path order (or a path repeats)")
		}
		prev = f.Path
		listed[f.Path] = f
	}
	actual, odd, err := walkBundle(dir)
	if err != nil {
		r.add(CodeFileMissing, "", fmt.Sprintf("reading the bundle: %v", err))
		return r.done()
	}
	r.Files = len(actual)
	for _, p := range odd {
		r.add(CodeFileTypeInvalid, p, "not a regular file")
	}
	have := map[string]File{}
	for _, f := range actual {
		have[f.Path] = f
	}
	for _, p := range slices.Sorted(mapKeys(listed)) {
		want := listed[p]
		got, ok := have[p]
		switch {
		case !ok:
			r.add(CodeFileMissing, p, "listed in the manifest, not in the bundle")
		case got.SHA256 != want.SHA256 || got.Bytes != want.Bytes:
			r.add(CodeFileModified, p, fmt.Sprintf("sha256 %s (%d bytes), manifest says %s (%d bytes)", got.SHA256, got.Bytes, want.SHA256, want.Bytes))
		}
	}
	for _, f := range actual {
		if _, ok := listed[f.Path]; !ok && !slices.Contains(odd, f.Path) {
			r.add(CodeFileUnlisted, f.Path, "in the bundle, not in the manifest")
		}
	}
	return r.done()
}

// readRegular reads a file that must be a regular file: a symbolic link is not followed.
func readRegular(p string) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", filepath.Base(p))
	}
	return os.ReadFile(p)
}

// parseManifest validates a v1 manifest's members (§5.2).
func parseManifest(m map[string]any) (Manifest, string, error) {
	var man Manifest
	f, err := str(m, "format")
	if err != nil {
		return man, CodeManifestMalformed, err
	}
	if f != FormatV1 {
		return man, CodeFormatUnsupported, fmt.Errorf("format %q", f)
	}
	v, err := integer(m, "format_version")
	if err != nil {
		return man, CodeManifestMalformed, err
	}
	if v != FormatVersion {
		return man, CodeFormatUnsupported, fmt.Errorf("format_version %d", v)
	}
	man.Format, man.FormatVersion = f, int(v)
	if man.BundleID, err = str(m, "bundle_id"); err == nil && !validBundleID(man.BundleID) {
		err = fmt.Errorf("bundle_id %q", man.BundleID)
	}
	if err != nil {
		return man, CodeManifestMalformed, err
	}
	if man.Created, err = str(m, "created"); err == nil && !validTime(man.Created) {
		err = fmt.Errorf("created %q: not an RFC 3339 UTC time", man.Created)
	}
	if err != nil {
		return man, CodeManifestMalformed, err
	}
	if man.KeyID, err = hex64(m, "key_id"); err != nil {
		return man, CodeManifestMalformed, err
	}
	subj, ok := m["subject"].(map[string]any)
	if !ok {
		return man, CodeManifestMalformed, errors.New(`"subject": missing or not an object`)
	}
	man.Subject = map[string]string{}
	for k, v := range subj {
		s, ok := v.(string)
		if !ok {
			return man, CodeManifestMalformed, fmt.Errorf("subject %q: not a string", k)
		}
		man.Subject[k] = s
	}
	files, ok := m["files"].([]any)
	if !ok {
		return man, CodeManifestMalformed, errors.New(`"files": missing or not an array`)
	}
	for i, x := range files {
		fo, ok := x.(map[string]any)
		if !ok {
			return man, CodeManifestMalformed, fmt.Errorf("files[%d]: not an object", i)
		}
		var f File
		if f.Path, err = str(fo, "path"); err != nil {
			return man, CodeManifestMalformed, fmt.Errorf("files[%d]: %w", i, err)
		}
		if f.SHA256, err = hex64(fo, "sha256"); err != nil {
			return man, CodeManifestMalformed, fmt.Errorf("files[%d]: %w", i, err)
		}
		if f.Bytes, err = integer(fo, "bytes"); err != nil {
			return man, CodeManifestMalformed, fmt.Errorf("files[%d]: %w", i, err)
		}
		man.Files = append(man.Files, f)
	}
	return man, "", nil
}

// decodeSignature accepts the base64 (standard alphabet, padded) of exactly 64 bytes and at most one line feed after it.
func decodeSignature(b []byte) ([]byte, bool) {
	b = bytes.TrimSuffix(b, []byte("\n"))
	sig, err := base64.StdEncoding.Strict().DecodeString(string(b))
	return sig, err == nil && len(sig) == ed25519.SignatureSize
}

// --- ledger ---

// Entry describes a bundle entry for Append, and the entry written.
type Entry struct {
	Seq          int       `json:"seq"`
	Time         time.Time `json:"time"`
	BundleID     string    `json:"bundle_id"`
	BundleSHA256 string    `json:"bundle_sha256"`
	Subject      string    `json:"subject"`
	Verdict      string    `json:"verdict"`
	KeyID        string    `json:"key_id"`
	Prev         string    `json:"prev"`
	EntrySHA256  string    `json:"entry_sha256"`
}

type payloadCommon struct {
	Format        string `json:"format"`
	FormatVersion int    `json:"format_version"`
	Seq           int    `json:"seq"`
	Time          string `json:"time"`
	Type          string `json:"type"`
	KeyID         string `json:"key_id"`
	Prev          string `json:"prev"`
}

type keyPayload struct {
	payloadCommon
	Event     string `json:"event"`
	PublicKey string `json:"public_key"`
	NewKeyID  string `json:"new_key_id,omitempty"`
}

type bundlePayload struct {
	payloadCommon
	BundleID     string `json:"bundle_id"`
	BundleSHA256 string `json:"bundle_sha256"`
	Subject      string `json:"subject"`
	Verdict      string `json:"verdict"`
}

type line struct {
	Payload string `json:"payload"`
	Sig     string `json:"sig"`
}

// signLine returns the ledger line for a payload: the payload's JSON as a string, and the signature of its bytes.
func signLine(p any, key ed25519.PrivateKey) ([]byte, string, error) {
	pb, err := json.Marshal(p)
	if err != nil {
		return nil, "", err
	}
	b, err := json.Marshal(line{Payload: string(pb), Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(key, pb))})
	if err != nil {
		return nil, "", err
	}
	return append(b, '\n'), sha256Hex(pb), nil
}

func common(seq int, t time.Time, typ, keyID, prev string) payloadCommon {
	return payloadCommon{Format: LedgerFormatV1, FormatVersion: FormatVersion, Seq: seq, Time: t.UTC().Format(time.RFC3339Nano),
		Type: typ, KeyID: keyID, Prev: prev}
}

// openLedger opens (creating) and locks the ledger and checks what is in it, for a writer signing with key.
func openLedger(path string) (*os.File, *ledgerScan, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if len(data) == 0 {
		return f, nil, nil
	}
	if isV0Ledger(data) {
		f.Close()
		return nil, nil, fmt.Errorf("ledger %s is a v0 ledger: read-only, start a new ledger", path)
	}
	s := scanLedger(data, nil, false)
	if len(s.findings) > 0 {
		f.Close()
		return nil, nil, fmt.Errorf("ledger %s is not intact, not appending: %s", path, strings.Join(findingText(s.findings), "; "))
	}
	return f, s, nil
}

func writeLines(f *os.File, lines ...[]byte) error {
	for _, l := range lines {
		if _, err := f.Write(l); err != nil {
			return err
		}
	}
	return f.Sync()
}

// Append adds a bundle entry, signed with key, to the ledger (created with a genesis entry for key if new),
// under an exclusive lock. key must be the ledger's current key.
func Append(ledger string, e Entry, key ed25519.PrivateKey) (Entry, error) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if !validBundleID(e.BundleID) {
		return e, fmt.Errorf("bundle id %q: want 1-255 of A-Z a-z 0-9 . _ -", e.BundleID)
	}
	if !hex64Re.MatchString(e.BundleSHA256) {
		return e, fmt.Errorf("bundle sha256 %q: want 64 lowercase hex digits", e.BundleSHA256)
	}
	f, s, err := openLedger(ledger)
	if err != nil {
		return e, err
	}
	defer f.Close()
	pub := key.Public().(ed25519.PublicKey)
	var lines [][]byte
	seq, prev := 1, zeroHash
	if s == nil { // a new ledger starts with the key that signs it
		g, h, err := signLine(keyPayload{payloadCommon: common(1, e.Time, "key", KeyID(pub), zeroHash), Event: "genesis",
			PublicKey: base64.StdEncoding.EncodeToString(pub)}, key)
		if err != nil {
			return e, err
		}
		lines, seq, prev = append(lines, g), 2, h
	} else {
		if !s.current.Equal(pub) {
			return e, fmt.Errorf("ledger %s: the current key is %s, not %s (rotate first)", ledger, KeyID(s.current), KeyID(pub))
		}
		seq, prev = s.lines+1, s.last
	}
	if _, dup := s.bundleIDs()[e.BundleID]; dup {
		return e, fmt.Errorf("ledger %s already has bundle %s", ledger, e.BundleID)
	}
	l, h, err := signLine(bundlePayload{payloadCommon: common(seq, e.Time, "bundle", KeyID(pub), prev), BundleID: e.BundleID,
		BundleSHA256: e.BundleSHA256, Subject: e.Subject, Verdict: e.Verdict}, key)
	if err != nil {
		return e, err
	}
	if err := writeLines(f, append(lines, l)...); err != nil {
		return e, err
	}
	e.Seq, e.KeyID, e.Prev, e.EntrySHA256 = seq, KeyID(pub), prev, h
	return e, nil
}

// Rotate appends a key rotation, signed by the current key old, that makes next the ledger's current key.
func Rotate(ledger string, old ed25519.PrivateKey, next ed25519.PublicKey) (Entry, error) {
	return RotateAt(ledger, old, next, time.Now())
}

// RotateAt is Rotate with a given time (for reproducible test vectors).
func RotateAt(ledger string, old ed25519.PrivateKey, next ed25519.PublicKey, t time.Time) (Entry, error) {
	f, s, err := openLedger(ledger)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	if s == nil {
		return Entry{}, fmt.Errorf("ledger %s is empty: there is no key to rotate", ledger)
	}
	pub := old.Public().(ed25519.PublicKey)
	switch {
	case !s.current.Equal(pub):
		return Entry{}, fmt.Errorf("ledger %s: the current key is %s, not %s", ledger, KeyID(s.current), KeyID(pub))
	case next.Equal(pub):
		return Entry{}, errors.New("the new key is the current key")
	}
	seq := s.lines + 1
	l, h, err := signLine(keyPayload{payloadCommon: common(seq, t, "key", KeyID(pub), s.last), Event: "rotate",
		PublicKey: base64.StdEncoding.EncodeToString(next), NewKeyID: KeyID(next)}, old)
	if err != nil {
		return Entry{}, err
	}
	if err := writeLines(f, l); err != nil {
		return Entry{}, err
	}
	return Entry{Seq: seq, Time: t, KeyID: KeyID(pub), Prev: s.last, EntrySHA256: h}, nil
}

func isV0Ledger(data []byte) bool {
	first, _, _ := bytes.Cut(data, []byte("\n"))
	var probe struct {
		Format string `json:"format"`
	}
	return json.Unmarshal(first, &probe) == nil && probe.Format == ledgerFormatV0
}

// bundleRef is a bundle entry found in the ledger, with the key that was current when it was written.
type bundleRef struct {
	seq                    int
	id, sha, subject, verd string
	signer                 ed25519.PublicKey
}

type ledgerScan struct {
	findings []Finding
	lines    int    // entries read (whether or not they passed)
	last     string // hash of the last entry read
	current  ed25519.PublicKey
	keys     []string
	bundles  []bundleRef
	// For head statements (head.go): per entry read, its hash and the key current after it; and every key that was
	// current at some point, by key_id.
	hashes      []string
	after       []ed25519.PublicKey
	known       map[string]ed25519.PublicKey
	unsupported bool // L2 stopped at an entry of a format_version it does not implement
}

func (s *ledgerScan) bundleIDs() map[string]bool {
	m := map[string]bool{}
	if s != nil {
		for _, b := range s.bundles {
			m[b.id] = true
		}
	}
	return m
}

func (s *ledgerScan) add(code string, n int, detail string) {
	s.findings = append(s.findings, Finding{Code: code, Severity: Severity(code), Where: fmt.Sprintf("line %d", n), Detail: detail})
}

// scanLedger is the specification's ledger procedure (§8.2, steps L2–L4) over the ledger's bytes. With anchor
// nil the genesis key is taken as given, and reported as anchor_missing if reportAnchor.
func scanLedger(data []byte, anchor ed25519.PublicKey, reportAnchor bool) *ledgerScan {
	s := &ledgerScan{known: map[string]ed25519.PublicKey{}}
	raw := bytes.Split(data, []byte("\n"))
	truncated := len(raw[len(raw)-1]) > 0
	if !truncated {
		raw = raw[:len(raw)-1]
	}
	for i, l := range raw {
		n := i + 1
		if truncated && i == len(raw)-1 {
			s.add(CodeEntryMalformed, n, "the last line has no line feed: the ledger is truncated")
			return s
		}
		var w struct {
			Payload *string `json:"payload"`
			Sig     *string `json:"sig"`
		}
		if wm, err := parseObject(l); err != nil {
			s.add(CodeEntryMalformed, n, err.Error())
			return s
		} else if _, ok := wm["payload"].(string); !ok {
			s.add(CodeEntryMalformed, n, `"payload": missing or not a string`)
			return s
		} else if _, ok := wm["sig"].(string); !ok {
			s.add(CodeEntryMalformed, n, `"sig": missing or not a string`)
			return s
		}
		if err := surrogates(l); err != nil {
			s.add(CodeEntryMalformed, n, err.Error())
			return s
		}
		json.Unmarshal(l, &w)
		pb := []byte(*w.Payload)
		p, err := parseObject(pb)
		if err != nil {
			s.add(CodeEntryMalformed, n, "payload: "+err.Error())
			return s
		}
		e, code, err := parsePayload(p)
		if err != nil {
			s.add(code, n, err.Error())
			s.unsupported = code == CodeFormatUnsupported
			return s
		}
		h := sha256Hex(pb)
		before := len(s.findings)
		if e.seq != int64(n) {
			s.add(CodeSeqInvalid, n, fmt.Sprintf("seq %d on line %d", e.seq, n))
		}
		wantPrev := zeroHash
		if n > 1 {
			wantPrev = s.last
		}
		if e.prev != wantPrev {
			s.add(CodeChainBroken, n, fmt.Sprintf("prev %s, the entry before hashes to %s", e.prev, wantPrev))
		}
		if n == 1 {
			if e.typ != "key" || e.event != "genesis" {
				s.add(CodeAnchorMismatch, n, "the first entry is not the genesis key entry")
				return s
			}
			pk, ok := decodeKey(e.publicKey)
			if !ok || KeyID(pk) != e.keyID {
				s.add(CodeKeyEntryInvalid, n, "public_key is not a base64 32-byte key whose key_id is key_id")
				return s
			}
			switch {
			case anchor == nil:
				if reportAnchor {
					s.add(CodeAnchorMissing, n, "no trust anchor given: the genesis key "+e.keyID+" is not checked against one")
				}
				anchor = pk
			case !anchor.Equal(pk):
				s.add(CodeAnchorMismatch, n, fmt.Sprintf("genesis key %s, trust anchor %s", e.keyID, KeyID(anchor)))
				return s
			}
			s.current, s.keys = anchor, []string{KeyID(anchor)}
			s.known[KeyID(anchor)] = anchor
		} else if e.keyID != KeyID(s.current) {
			s.add(CodeSignerInvalid, n, fmt.Sprintf("key_id %s, the current key is %s", e.keyID, KeyID(s.current)))
		}
		if sig, ok := decodeSig64(*w.Sig); !ok || !ed25519.Verify(s.current, pb, sig) {
			s.add(CodeEntrySignatureInvalid, n, "sig is not the current key's signature of the payload's bytes")
		}
		var next ed25519.PublicKey
		switch {
		case e.typ == "key" && e.event == "genesis" && n > 1:
			s.add(CodeKeyEntryInvalid, n, "a genesis entry after line 1")
		case e.typ == "key" && e.event == "rotate":
			pk, ok := decodeKey(e.publicKey)
			switch {
			case !ok || KeyID(pk) != e.newKeyID:
				s.add(CodeKeyEntryInvalid, n, "public_key is not a base64 32-byte key whose key_id is new_key_id")
			case pk.Equal(s.current):
				s.add(CodeKeyEntryInvalid, n, "the new key is the current key")
			default:
				next = pk
			}
		case e.typ == "bundle":
			if s.bundleIDs()[e.bundleID] {
				s.add(CodeBundleDuplicate, n, "bundle "+e.bundleID+" has an earlier entry")
			}
			s.bundles = append(s.bundles, bundleRef{seq: n, id: e.bundleID, sha: e.bundleSHA, subject: e.subject, verd: e.verdict, signer: s.current})
		}
		if next != nil && len(s.findings) == before { // a rotation counts only when its entry passed every check
			s.current, s.keys = next, append(s.keys, KeyID(next))
			s.known[KeyID(next)] = next
		}
		s.lines, s.last = n, h
		s.hashes, s.after = append(s.hashes, h), append(s.after, s.current)
	}
	return s
}

// surrogates rejects a \u escape of a UTF-16 surrogate that is not part of a pair: it has no UTF-8 encoding,
// so the payload bytes it stands for would depend on the JSON library (§6.2).
func surrogates(b []byte) error {
	high := false
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			if high {
				return errors.New("a high surrogate escape without a low one")
			}
			continue
		}
		i++
		if i >= len(b) || b[i] != 'u' || i+4 >= len(b) {
			if high {
				return errors.New("a high surrogate escape without a low one")
			}
			continue
		}
		var v uint16
		if _, err := fmt.Sscanf(string(b[i+1:i+5]), "%04x", &v); err != nil {
			return errors.New("a bad \\u escape")
		}
		i += 4
		switch {
		case v >= 0xd800 && v <= 0xdbff:
			if high {
				return errors.New("two high surrogate escapes in a row")
			}
			high = true
		case v >= 0xdc00 && v <= 0xdfff:
			if !high {
				return errors.New("a low surrogate escape without a high one")
			}
			high = false
		default:
			if high {
				return errors.New("a high surrogate escape without a low one")
			}
		}
	}
	if high {
		return errors.New("a high surrogate escape at the end")
	}
	return nil
}

type payloadFields struct {
	seq                                    int64
	typ, keyID, prev, event, publicKey     string
	newKeyID, bundleID, bundleSHA, subject string
	verdict                                string
}

// parsePayload validates an entry payload's members (§6.3).
func parsePayload(p map[string]any) (payloadFields, string, error) {
	var e payloadFields
	f, err := str(p, "format")
	if err != nil {
		return e, CodeEntryMalformed, err
	}
	if f != LedgerFormatV1 {
		return e, CodeEntryMalformed, fmt.Errorf("format %q", f)
	}
	v, err := integer(p, "format_version")
	if err != nil {
		return e, CodeEntryMalformed, err
	}
	if v != FormatVersion {
		return e, CodeFormatUnsupported, fmt.Errorf("format_version %d", v)
	}
	bad := func(err error) (payloadFields, string, error) { return e, CodeEntryMalformed, err }
	if e.seq, err = integer(p, "seq"); err != nil {
		return bad(err)
	}
	if e.seq < 1 {
		return bad(errors.New(`"seq": less than 1`))
	}
	t, err := str(p, "time")
	if err == nil && !validTime(t) {
		err = fmt.Errorf("time %q: not an RFC 3339 UTC time", t)
	}
	if err != nil {
		return bad(err)
	}
	if e.typ, err = str(p, "type"); err != nil {
		return bad(err)
	}
	if e.keyID, err = hex64(p, "key_id"); err != nil {
		return bad(err)
	}
	if e.prev, err = hex64(p, "prev"); err != nil {
		return bad(err)
	}
	switch e.typ {
	case "key":
		if e.event, err = str(p, "event"); err != nil {
			return bad(err)
		}
		if e.publicKey, err = str(p, "public_key"); err != nil {
			return bad(err)
		}
		switch e.event {
		case "genesis":
		case "rotate":
			if e.newKeyID, err = hex64(p, "new_key_id"); err != nil {
				return bad(err)
			}
		default:
			return bad(fmt.Errorf("event %q", e.event))
		}
	case "bundle":
		if e.bundleID, err = str(p, "bundle_id"); err == nil && !validBundleID(e.bundleID) {
			err = fmt.Errorf("bundle_id %q", e.bundleID)
		}
		if err != nil {
			return bad(err)
		}
		if e.bundleSHA, err = hex64(p, "bundle_sha256"); err != nil {
			return bad(err)
		}
		if e.subject, err = str(p, "subject"); err != nil {
			return bad(err)
		}
		if e.verdict, err = str(p, "verdict"); err != nil {
			return bad(err)
		}
	default:
		return bad(fmt.Errorf("type %q", e.typ))
	}
	return e, "", nil
}

func decodeKey(s string) (ed25519.PublicKey, bool) {
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	return ed25519.PublicKey(b), err == nil && len(b) == ed25519.PublicKeySize
}

func decodeSig64(s string) ([]byte, bool) {
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	return b, err == nil && len(b) == ed25519.SignatureSize
}

// VerifyLedger checks a ledger against a trust anchor (nil: none, which leaves the verdict at best UNKNOWN),
// every bundle it lists in bundlesDir/<bundle_id>, and that no bundle directory lacks an entry (§8.2).
// (A ledger cut off together with its newest bundles still verifies: that needs an anchor outside.)
func VerifyLedger(ledger, bundlesDir string, anchor ed25519.PublicKey) (LedgerReport, error) {
	return VerifyLedgerHeads(ledger, bundlesDir, anchor, nil)
}

// VerifyLedgerHeads is VerifyLedger with head statements kept outside the ledger (§8.6): each must name an entry the
// ledger still has, unchanged; a ledger shorter than a statement is ledger_truncated.
func VerifyLedgerHeads(ledger, bundlesDir string, anchor ed25519.PublicKey, heads [][]byte) (LedgerReport, error) {
	r := &LedgerReport{Ledger: ledger, Format: "v1", Findings: []Finding{}, Bundles: []BundleReport{}}
	data, err := os.ReadFile(ledger)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.add(CodeLedgerMissing, "", err.Error())
		r.checkHeads(heads, nil, anchor)
		r.unlisted(bundlesDir, nil)
		return r.done(), nil
	case err != nil:
		return r.done(), err
	case len(data) == 0:
		r.add(CodeLedgerEmpty, "", "the ledger has no entries")
		r.checkHeads(heads, nil, anchor)
		r.unlisted(bundlesDir, nil)
		return r.done(), nil
	case isV0Ledger(data):
		verifyLedgerV0(r, data, bundlesDir, anchor)
		for i := range heads {
			r.add(CodeFormatUnsupported, fmt.Sprintf("head %d", i+1), "head statements need a version 1 ledger")
		}
		return r.done(), nil
	}
	s := scanLedger(data, anchor, true)
	r.checkHeads(heads, s, anchor)
	r.Entries, r.Keys, r.Findings = s.lines, s.keys, append(r.Findings, s.findings...)
	listed := map[string]bool{}
	for _, b := range s.bundles {
		listed[b.id] = true
		dir := filepath.Join(bundlesDir, b.id)
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() { // a symbolic link is not a bundle
			r.add(CodeBundleMissing, b.id, fmt.Sprintf("line %d names bundle %s; there is no such directory", b.seq, b.id))
			continue
		}
		br := verifyBundle(dir, b.signer)
		switch {
		case br.Format == "v0":
			br = *newBundleReport(dir, "v0")
			br.add(CodeFormatUnsupported, ManifestFile, "a v0 bundle in a v1 ledger")
		case br.SHA256 != "": // manifest.json was read: it must be the one the entry recorded
			if br.SHA256 != b.sha {
				br.add(CodeBundleHashMismatch, ManifestFile, fmt.Sprintf("manifest.json hashes to %s, line %d recorded %s", br.SHA256, b.seq, b.sha))
			}
			if br.BundleID != "" && br.BundleID != b.id {
				br.add(CodeBundleIDMismatch, ManifestFile, fmt.Sprintf("manifest names bundle %s, line %d %s", br.BundleID, b.seq, b.id))
			}
		}
		if br.BundleID == "" {
			br.BundleID = b.id
		}
		r.Bundles = append(r.Bundles, br.done())
	}
	r.unlisted(bundlesDir, listed)
	return r.done(), nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// GenesisKey returns the key a ledger's first entry declares: the key a verifier must already trust (§6.4). It does
// not verify the ledger; VerifyLedger does.
func GenesisKey(ledger string) (ed25519.PublicKey, error) {
	f, err := os.Open(ledger)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	first, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("ledger %s: no first line: %w", ledger, err)
	}
	var w line
	if err := json.Unmarshal(first, &w); err != nil {
		return nil, fmt.Errorf("ledger %s: line 1: %w", ledger, err)
	}
	p, err := parseObject([]byte(w.Payload))
	if err != nil {
		return nil, fmt.Errorf("ledger %s: line 1: %w", ledger, err)
	}
	e, _, err := parsePayload(p)
	if err != nil || e.typ != "key" || e.event != "genesis" {
		return nil, fmt.Errorf("ledger %s: line 1 is not a genesis entry (%v)", ledger, err)
	}
	pk, ok := decodeKey(e.publicKey)
	if !ok {
		return nil, fmt.Errorf("ledger %s: line 1: bad public_key", ledger)
	}
	return pk, nil
}
