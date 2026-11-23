package evidence

import (
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
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The evidence export (ADR-007 C7) is a directory: the ledger, its bundles, the trust anchor, reports, and a
// SHA256SUMS that lists the hash of every other file. SHA256SUMS is signed by the installation's evidence key, the
// key that signs the ledger, in SHA256SUMS.sig. This is not part of the Restore Evidence Format: it protects the
// files the format does not cover (the README, the daily report, the test summary) in transit. It is not a trust
// anchor either: the signing key is trusted only because the ledger, verified from the trust anchor, leads to it, and
// the anchor's key_id is compared with a daily report received outside the export first.
//
// SHA256SUMS: one line per file, "<hex64>  <path>\n" (GNU coreutils' format; `sha256sum -c` checks it), paths
// relative to the export's directory with "/" between segments, sorted byte-wise.
//
// SHA256SUMS.sig: one JSON object (§2's rules: UTF-8, no repeated member), indented, with a final line feed:
//
//	format          "gaia-sha256sums-signature"
//	format_version  1
//	file            "SHA256SUMS"
//	sha256          hex64 of SHA256SUMS's bytes
//	signed          when it was signed (RFC 3339 UTC, informational)
//	key_id          hex64: the key_id of public_key (specification §2)
//	public_key      base64 of the 32-byte Ed25519 public key (the specification's key form, §3)
//	signature       base64 of the Ed25519 signature (pure Ed25519, as in §2) of the exact bytes of SHA256SUMS
//
// Unknown members are ignored. Any Ed25519 implementation checks it: decode public_key, check that its SHA-256 is
// key_id, verify signature over SHA256SUMS's bytes.

// Names and identifiers of the signed SHA256SUMS.
const (
	SumsFile            = "SHA256SUMS"
	SumsSignatureFile   = "SHA256SUMS.sig"
	SumsSignatureFormat = "gaia-sha256sums-signature"
)

// Finding codes of an export's SHA256SUMS and its signature (Gaia's, not the specification's §8.3).
const (
	CodeSumsMissing              = "sums_missing"               // CRITICAL: SHA256SUMS cannot be read
	CodeSumsMalformed            = "sums_malformed"             // CRITICAL: a line is not "<hex64>  <path>", or a path is invalid or repeated
	CodeSumsFileMissing          = "sums_file_missing"          // CRITICAL: a listed file is not a regular file in the export
	CodeSumsFileModified         = "sums_file_modified"         // CRITICAL: a listed file's hash differs
	CodeSumsFileUnlisted         = "sums_file_unlisted"         // CRITICAL: a regular file of the export is not listed
	CodeSumsFileTypeInvalid      = "sums_file_type_invalid"     // CRITICAL: the export holds a symbolic link, a device, ...
	CodeSumsSignatureMissing     = "sums_signature_missing"     // UNKNOWN: no SHA256SUMS.sig (an export made before 0.2.0): not checked
	CodeSumsSignatureMalformed   = "sums_signature_malformed"   // CRITICAL: SHA256SUMS.sig breaks its format
	CodeSumsSignatureUnsupported = "sums_signature_unsupported" // UNKNOWN: a format_version this verifier does not implement
	CodeSumsSignatureInvalid     = "sums_signature_invalid"     // CRITICAL: the signature or its sha256 does not match SHA256SUMS
	CodeSumsKeyMismatch          = "sums_key_mismatch"          // CRITICAL: signed by a key that is not the ledger's current key
	CodeSumsKeyUnverifiable      = "sums_key_unverifiable"      // UNKNOWN: the ledger leads to no key to compare with
)

func sumsSeverity(code string) string {
	switch code {
	case CodeSumsSignatureMissing, CodeSumsSignatureUnsupported, CodeSumsKeyUnverifiable:
		return Unknown
	}
	return Critical
}

// SumsSignature is SHA256SUMS.sig.
type SumsSignature struct {
	Format        string `json:"format"`
	FormatVersion int    `json:"format_version"`
	File          string `json:"file"`
	SHA256        string `json:"sha256"`
	Signed        string `json:"signed"`
	KeyID         string `json:"key_id"`
	PublicKey     string `json:"public_key"`
	Signature     string `json:"signature"`
}

// SumsLines lays out SHA256SUMS from path -> hex64, sorted by path.
func SumsLines(sums map[string]string) []byte {
	names := slices.Sorted(mapKeys(sums))
	var b bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[n], n)
	}
	return b.Bytes()
}

// SignSums signs the bytes of SHA256SUMS with key and returns SHA256SUMS.sig.
func SignSums(sums []byte, key ed25519.PrivateKey, t time.Time) ([]byte, error) {
	pub := key.Public().(ed25519.PublicKey)
	s := SumsSignature{Format: SumsSignatureFormat, FormatVersion: 1, File: SumsFile, SHA256: sha256Hex(sums),
		Signed: t.UTC().Format(time.RFC3339), KeyID: KeyID(pub), PublicKey: base64.StdEncoding.EncodeToString(pub),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, sums))}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// SumsReport is the check of an export's SHA256SUMS and SHA256SUMS.sig.
type SumsReport struct {
	SHA256   string `json:"sha256"`   // of SHA256SUMS ("" when it cannot be read)
	Listed   int    `json:"listed"`   // files SHA256SUMS lists
	Matching int    `json:"matching"` // of them, present with the listed hash
	Signed   string `json:"signed"`   // when the signature says it was made
	KeyID    string `json:"key_id"`   // the key that signed it ("" without a readable signature)
	// SignatureValid: the signature verifies with the key named by KeyID. Whether that key is to be trusted is the
	// export report's sums_key_* finding (the ledger's current key) and, first, the comparison of the anchor's key_id
	// with an externally received daily report.
	SignatureValid bool      `json:"signature_valid"`
	Findings       []Finding `json:"findings"`
}

func (r *SumsReport) add(code, where, detail string) {
	r.Findings = append(r.Findings, Finding{Code: code, Severity: sumsSeverity(code), Where: where, Detail: detail})
}

// ExportRecord is an evidence export recorded in the ledger (ADR-007 C7): a bundle whose manifest's subject has kind
// "evidence_export".
type ExportRecord struct {
	BundleID    string `json:"bundle_id"`
	LedgerSeq   int    `json:"ledger_seq"`
	Verdict     string `json:"verdict"` // the bundle's verification
	User        string `json:"user"`
	Scope       string `json:"scope"`
	PeriodFrom  string `json:"period_from"`
	PeriodTo    string `json:"period_to"`
	SumsSHA256  string `json:"sha256sums_sha256"`
	Export      string `json:"export"` // the export's directory name
	CreatedTime string `json:"created"`
}

// ExportReport is the verification of an export directory: SHA256SUMS against the files, its signature against the
// ledger's current key, and the ledger with every bundle and head statement (specification §8.2).
type ExportReport struct {
	Dir       string         `json:"dir"`
	Verdict   string         `json:"verdict"`
	Sums      SumsReport     `json:"sums"`
	HeadFiles []string       `json:"head_files"` // the head statements checked: the export's own, then those given
	Ledger    LedgerReport   `json:"ledger"`
	Exports   []ExportRecord `json:"recorded_exports"` // earlier exports this ledger records
}

// AllFindings returns the findings of SHA256SUMS, its signature, the ledger and its bundles.
func (r ExportReport) AllFindings() []Finding {
	return append(slices.Clone(r.Sums.Findings), r.Ledger.AllFindings()...)
}

// The export's files with a fixed place.
const (
	ExportLedgerFile  = "ledger.jsonl"
	ExportBundlesDir  = "bundles"
	ExportHeadPattern = "ledger-head-*.json"
)

// VerifyExport checks an evidence export directory (ADR-007 C7) against the trust anchor (nil: none, and the verdict
// is at best UNKNOWN): every file against SHA256SUMS, SHA256SUMS.sig against SHA256SUMS and the ledger's current key,
// and the ledger with its bundles (§8.2) and head statements: the export's own ledger-head-*.json, then heads. An
// error means the verification could not run (no verdict; a four-level tool reports UNKNOWN).
func VerifyExport(dir string, anchor ed25519.PublicKey, heads [][]byte) (ExportReport, error) {
	r := ExportReport{Dir: dir, HeadFiles: []string{}, Exports: []ExportRecord{}}
	if fi, err := os.Stat(dir); err != nil {
		return r, err
	} else if !fi.IsDir() {
		return r, fmt.Errorf("%s: not a directory", dir)
	}
	own, err := filepath.Glob(filepath.Join(dir, ExportHeadPattern))
	if err != nil {
		return r, err
	}
	slices.Sort(own)
	var statements [][]byte
	for _, h := range own {
		b, err := os.ReadFile(h)
		if err != nil {
			return r, err
		}
		statements = append(statements, HeadFromDocument(b))
		r.HeadFiles = append(r.HeadFiles, filepath.Base(h))
	}
	for i, h := range heads {
		statements = append(statements, h)
		r.HeadFiles = append(r.HeadFiles, fmt.Sprintf("(given %d)", i+1))
	}
	if r.Ledger, err = VerifyLedgerHeads(filepath.Join(dir, ExportLedgerFile), filepath.Join(dir, ExportBundlesDir), anchor, statements); err != nil {
		return r, err
	}
	r.Sums = checkSums(dir, r.Ledger.Keys)
	if r.Exports, err = recordedExports(r.Ledger, filepath.Join(dir, ExportBundlesDir)); err != nil {
		return r, err
	}
	r.Verdict = Verdict(r.AllFindings())
	return r, nil
}

// checkSums checks SHA256SUMS against the directory's files and SHA256SUMS.sig against SHA256SUMS; keys are the
// key_ids the ledger made current, in order (the last one signs now).
func checkSums(dir string, keys []string) SumsReport {
	r := SumsReport{Findings: []Finding{}}
	sums, err := readRegular(filepath.Join(dir, SumsFile))
	if err != nil {
		r.add(CodeSumsMissing, SumsFile, err.Error())
		return r
	}
	r.SHA256 = sha256Hex(sums)
	listed := map[string]string{}
	if len(sums) > 0 && sums[len(sums)-1] != '\n' {
		r.add(CodeSumsMalformed, SumsFile, "the last line has no line feed")
	}
	for i, l := range strings.Split(strings.TrimSuffix(string(sums), "\n"), "\n") {
		if l == "" && len(sums) == 0 {
			break
		}
		where := fmt.Sprintf("%s line %d", SumsFile, i+1)
		sum, name, ok := strings.Cut(l, "  ")
		switch {
		case !ok || !hex64Re.MatchString(sum):
			r.add(CodeSumsMalformed, where, `not "<64 lowercase hex digits>  <path>"`)
			continue
		case !validSumsPath(name):
			r.add(CodeSumsMalformed, where, fmt.Sprintf("path %q: not a relative path inside the export", name))
			continue
		case listed[name] != "":
			r.add(CodeSumsMalformed, where, fmt.Sprintf("path %q is listed twice", name))
			continue
		}
		listed[name] = sum
	}
	r.Listed = len(listed)
	present := map[string]bool{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case p == dir || d.IsDir() || rel == SumsFile || rel == SumsSignatureFile:
			return nil
		case !d.Type().IsRegular():
			r.add(CodeSumsFileTypeInvalid, rel, "neither a directory nor a regular file")
			return nil
		}
		present[rel] = true
		want, ok := listed[rel]
		if !ok {
			r.add(CodeSumsFileUnlisted, rel, "a file that SHA256SUMS does not list")
			return nil
		}
		got, err := fileSHA256(p)
		if err != nil {
			return err
		}
		if got != want {
			r.add(CodeSumsFileModified, rel, fmt.Sprintf("hashes to %s, SHA256SUMS lists %s", got, want))
			return nil
		}
		r.Matching++
		return nil
	})
	if err != nil {
		r.add(CodeSumsFileMissing, "", "the export cannot be read: "+err.Error())
	}
	for _, name := range slices.Sorted(mapKeys(listed)) {
		if !present[name] {
			r.add(CodeSumsFileMissing, name, "SHA256SUMS lists it; it is not a regular file in the export")
		}
	}
	checkSumsSignature(&r, dir, sums, keys)
	return r
}

func checkSumsSignature(r *SumsReport, dir string, sums []byte, keys []string) {
	b, err := readRegular(filepath.Join(dir, SumsSignatureFile))
	if errors.Is(err, fs.ErrNotExist) {
		r.add(CodeSumsSignatureMissing, SumsSignatureFile, "no signature: the file hashes were not checked against the installation's key (exports made before Gaia 0.2.0 have none)")
		return
	}
	if err != nil {
		r.add(CodeSumsSignatureMalformed, SumsSignatureFile, err.Error())
		return
	}
	m, err := parseObject(b)
	if err != nil {
		r.add(CodeSumsSignatureMalformed, SumsSignatureFile, err.Error())
		return
	}
	bad := func(err error) { r.add(CodeSumsSignatureMalformed, SumsSignatureFile, err.Error()) }
	f, err := str(m, "format")
	if err == nil && f != SumsSignatureFormat {
		err = fmt.Errorf("format %q, want %q", f, SumsSignatureFormat)
	}
	if err != nil {
		bad(err)
		return
	}
	v, err := integer(m, "format_version")
	if err != nil {
		bad(err)
		return
	}
	if v != 1 {
		r.add(CodeSumsSignatureUnsupported, SumsSignatureFile, fmt.Sprintf("format_version %d", v))
		return
	}
	var s SumsSignature
	if s.File, err = str(m, "file"); err == nil && s.File != SumsFile {
		err = fmt.Errorf("file %q, want %q", s.File, SumsFile)
	}
	if err != nil {
		bad(err)
		return
	}
	if s.SHA256, err = hex64(m, "sha256"); err != nil {
		bad(err)
		return
	}
	if s.Signed, err = str(m, "signed"); err == nil && !validTime(s.Signed) {
		err = fmt.Errorf("signed %q: not an RFC 3339 UTC time", s.Signed)
	}
	if err != nil {
		bad(err)
		return
	}
	if s.KeyID, err = hex64(m, "key_id"); err != nil {
		bad(err)
		return
	}
	if s.PublicKey, err = str(m, "public_key"); err != nil {
		bad(err)
		return
	}
	pub, ok := decodeKey(s.PublicKey)
	if !ok || KeyID(pub) != s.KeyID {
		bad(errors.New("public_key is not the base64 of a 32-byte key whose key_id is key_id"))
		return
	}
	if s.Signature, err = str(m, "signature"); err != nil {
		bad(err)
		return
	}
	sig, ok := decodeSig64(s.Signature)
	if !ok {
		bad(errors.New("signature is not the base64 of 64 bytes"))
		return
	}
	r.Signed, r.KeyID = s.Signed, s.KeyID
	switch {
	case s.SHA256 != r.SHA256:
		r.add(CodeSumsSignatureInvalid, SumsSignatureFile, fmt.Sprintf("signs a SHA256SUMS that hashes to %s; this one hashes to %s", s.SHA256, r.SHA256))
	case !ed25519.Verify(pub, sums, sig):
		r.add(CodeSumsSignatureInvalid, SumsSignatureFile, "signature is not key "+s.KeyID+"'s signature of SHA256SUMS")
	default:
		r.SignatureValid = true
	}
	switch {
	case len(keys) == 0:
		r.add(CodeSumsKeyUnverifiable, SumsSignatureFile, "the ledger leads to no key: whether key "+s.KeyID+" is the installation's cannot be checked")
	case keys[len(keys)-1] != s.KeyID:
		was := ""
		if slices.Contains(keys, s.KeyID) {
			was = " (a key the ledger rotated away from)"
		}
		r.add(CodeSumsKeyMismatch, SumsSignatureFile, fmt.Sprintf("signed by key %s%s; the ledger's current key is %s", s.KeyID, was, keys[len(keys)-1]))
	}
}

// validSumsPath: a relative, slash-separated path without empty, "." or ".." segments, backslashes, NUL or line
// breaks, that is not SHA256SUMS or its signature.
func validSumsPath(p string) bool {
	if p == "" || p == SumsFile || p == SumsSignatureFile || !utf8.ValidString(p) || strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recordedExports lists the ledger's bundles that record an evidence export, with their verification. A bundle whose
// manifest cannot be read or parsed is not listed: the ledger's verification already reports it.
func recordedExports(l LedgerReport, bundlesDir string) ([]ExportRecord, error) {
	out := []ExportRecord{}
	if len(l.Bundles) == 0 {
		return out, nil
	}
	data, err := os.ReadFile(l.Ledger)
	if err != nil {
		return out, err
	}
	seq := bundleSeqs(data)
	for _, b := range l.Bundles {
		if b.Format != "v1" {
			continue
		}
		mb, err := readRegular(filepath.Join(bundlesDir, b.BundleID, ManifestFile))
		if err != nil {
			continue
		}
		m, err := parseObject(mb)
		if err != nil {
			continue
		}
		man, _, err := parseManifest(m)
		if err != nil || man.Subject["kind"] != ExportBundleKind {
			continue
		}
		sj := man.Subject
		out = append(out, ExportRecord{BundleID: b.BundleID, LedgerSeq: seq[b.BundleID], Verdict: b.Verdict, User: sj["user"], Scope: sj["scope"],
			PeriodFrom: sj["period_from"], PeriodTo: sj["period_to"], SumsSHA256: sj["sha256sums_sha256"], Export: sj["export"], CreatedTime: man.Created})
	}
	return out, nil
}

// ExportBundleKind is the subject kind of a bundle that records an evidence export.
const ExportBundleKind = "evidence_export"

// BundleEntry returns the line of the ledger's entry for bundleID, 0 when it has none. It does not verify the ledger.
func BundleEntry(ledger, bundleID string) (int, error) {
	data, err := os.ReadFile(ledger)
	if err != nil {
		return 0, err
	}
	return bundleSeqs(data)[bundleID], nil
}

// bundleSeqs maps a ledger's bundle_ids to their lines. Lines it cannot read are skipped: it describes, a
// verification judges.
func bundleSeqs(data []byte) map[string]int {
	out := map[string]int{}
	for i, l := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var w struct {
			Payload string `json:"payload"`
		}
		var p struct {
			Type     string `json:"type"`
			BundleID string `json:"bundle_id"`
		}
		if json.Unmarshal(l, &w) == nil && json.Unmarshal([]byte(w.Payload), &p) == nil && p.Type == "bundle" && p.BundleID != "" {
			out[p.BundleID] = i + 1
		}
	}
	return out
}
