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
//
// EXPORT.json (Gaia 0.2.0 and later; listed in SHA256SUMS like every other file) is the export's own manifest: format
// "gaia-evidence-export", who made it, its scope (kind "all": every bundle; kind "bundle" with "bundle": one bundle and
// the whole ledger, ADR-007 C18.6) and period, and ledger_record: {state, bundle_id, reason}, where state is
// sealed_after_export (a full export, recorded as bundle_id once complete), not_recorded (a full export the ledger
// could not record: reason; the server appends bundle_id with its next successful ledger write, ADR-007 C18.5) or
// audit_only (a single-bundle export: an audit log entry, no ledger record). Unknown members are ignored.

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

// Finding codes of what an export states about itself (EXPORT.json, ADR-007 C18.5): never OK, never CRITICAL. The
// record of an export is sealed after the export is complete, so no export holds its own record; these codes say what
// the export itself states.
const (
	CodeExportNotRecorded       = "export_not_recorded"       // UNKNOWN: the ledger could not record the export when it was given
	CodeExportManifestMalformed = "export_manifest_malformed" // UNKNOWN: EXPORT.json cannot be read: its scope and record are not known
)

func sumsSeverity(code string) string {
	switch code {
	case CodeSumsSignatureMissing, CodeSumsSignatureUnsupported, CodeSumsKeyUnverifiable, CodeExportNotRecorded, CodeExportManifestMalformed:
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
	// NotRecordedReason: the export was given while the ledger could not record it (ADR-007 C18.5), and this record
	// was appended later, with the next successful ledger write; why the ledger refused it then. "" for the others.
	NotRecordedReason string `json:"not_recorded_reason,omitempty"`
}

// ExportOwnRecord is what an export states about itself in EXPORT.json (Gaia 0.2.0 and later): what it holds and its
// record. No export holds its own record (it is sealed after the export is complete), so this is what the export
// says, not something its ledger shows; a not_recorded export is UNKNOWN (export_not_recorded), never OK.
type ExportOwnRecord struct {
	Manifest string `json:"manifest"` // EXPORT.json, or "" when the export has none (made before 0.2.0)
	Scope    string `json:"scope"`    // all (every bundle) or bundle (one bundle, the whole ledger); all when not stated
	Bundle   string `json:"bundle,omitempty"`
	// State: sealed_after_export (recorded as BundleID once the export was complete: the server's ledger and every
	// later export hold it), not_recorded (the ledger refused the record; Gaia appends BundleID with the next
	// successful ledger write), audit_only (a single-bundle export: an audit log entry, no ledger record), or ""
	// (not stated: an export made before 0.2.0).
	State    string    `json:"state"`
	BundleID string    `json:"bundle_id,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Findings []Finding `json:"findings"`
}

// ExportReport is the verification of an export directory: SHA256SUMS against the files, its signature against the
// ledger's current key, and the ledger with every bundle and head statement (specification §8.2).
type ExportReport struct {
	Dir       string          `json:"dir"`
	Verdict   string          `json:"verdict"`
	Sums      SumsReport      `json:"sums"`
	HeadFiles []string        `json:"head_files"` // the head statements checked: the export's own, then those given
	Ledger    LedgerReport    `json:"ledger"`
	Exports   []ExportRecord  `json:"recorded_exports"` // earlier exports this ledger records
	Record    ExportOwnRecord `json:"record"`           // what the export states about itself (EXPORT.json)
}

// AllFindings returns the findings of SHA256SUMS, its signature, the export's own record, the ledger and its bundles.
func (r ExportReport) AllFindings() []Finding {
	return append(append(slices.Clone(r.Sums.Findings), r.Record.Findings...), r.Ledger.AllFindings()...)
}

// The export's files with a fixed place.
const (
	ExportLedgerFile  = "ledger.jsonl"
	ExportBundlesDir  = "bundles"
	ExportHeadPattern = "ledger-head-*.json"
	// ExportManifestFile is the export's own manifest (Gaia 0.2.0 and later): its format, who made it, its scope and
	// period, and its record in the ledger (member ledger_record). SHA256SUMS lists it like every other file.
	ExportManifestFile = "EXPORT.json"
)

// EXPORT.json's format, scopes and record states.
const (
	ExportManifestFormat    = "gaia-evidence-export"
	ExportScopeAll          = "all"
	ExportScopeBundle       = "bundle"
	ExportRecordSealedAfter = "sealed_after_export"
	ExportRecordNotRecorded = "not_recorded"
	ExportRecordAuditOnly   = "audit_only"
)

// readExportManifest reads the export's EXPORT.json. Without one (an export made before 0.2.0) the scope is all and
// the record is not stated. One that cannot be read is export_manifest_malformed, and the scope is all: every bundle
// the ledger lists must then be present.
func readExportManifest(dir string) ExportOwnRecord {
	r := ExportOwnRecord{Scope: ExportScopeAll, Findings: []Finding{}}
	bad := func(err error) ExportOwnRecord {
		r.Scope, r.Bundle, r.State, r.BundleID, r.Reason = ExportScopeAll, "", "", "", ""
		r.Findings = append(r.Findings, Finding{Code: CodeExportManifestMalformed, Severity: sumsSeverity(CodeExportManifestMalformed), Where: ExportManifestFile,
			Detail: err.Error() + ": what the export holds and its record are not known; every bundle the ledger lists is required"})
		return r
	}
	b, err := readRegular(filepath.Join(dir, ExportManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return r
	}
	r.Manifest = ExportManifestFile
	if err != nil {
		return bad(err)
	}
	m, err := parseObject(b)
	if err != nil {
		return bad(err)
	}
	if f, err := str(m, "format"); err != nil || f != ExportManifestFormat {
		return bad(fmt.Errorf("format: want %q", ExportManifestFormat))
	}
	sc, ok := m["scope"].(map[string]any)
	if !ok {
		return bad(errors.New("scope: not an object"))
	}
	if r.Scope, err = str(sc, "kind"); err != nil {
		return bad(fmt.Errorf("scope: %w", err))
	}
	switch r.Scope {
	case ExportScopeAll:
	case ExportScopeBundle:
		if r.Bundle, err = str(sc, "bundle"); err != nil || !validBundleID(r.Bundle) {
			return bad(errors.New("scope: bundle: not a bundle id"))
		}
	default:
		return bad(fmt.Errorf("scope: kind %q (all or bundle)", r.Scope))
	}
	lr, ok := m["ledger_record"].(map[string]any)
	if !ok {
		return bad(errors.New("ledger_record: not an object"))
	}
	if r.State, err = str(lr, "state"); err != nil {
		return bad(fmt.Errorf("ledger_record: %w", err))
	}
	if _, ok := lr["bundle_id"]; ok {
		if r.BundleID, err = str(lr, "bundle_id"); err != nil || !validBundleID(r.BundleID) {
			return bad(errors.New("ledger_record: bundle_id: not a bundle id"))
		}
	}
	if _, ok := lr["reason"]; ok {
		if r.Reason, err = str(lr, "reason"); err != nil {
			return bad(fmt.Errorf("ledger_record: %w", err))
		}
	}
	switch {
	case r.State == ExportRecordSealedAfter && r.Scope == ExportScopeAll && r.BundleID != "":
	case r.State == ExportRecordAuditOnly && r.Scope == ExportScopeBundle:
	case r.State == ExportRecordNotRecorded && r.Scope == ExportScopeAll:
		later := "the server could not keep its record either: the audit log entry is its only record"
		if r.BundleID != "" {
			later = "the server keeps its record and appends it as bundle " + r.BundleID +
				" with its next successful ledger write: a later export lists it under its recorded exports"
		}
		r.Findings = append(r.Findings, Finding{Code: CodeExportNotRecorded, Severity: sumsSeverity(CodeExportNotRecorded), Where: ExportManifestFile,
			Detail: fmt.Sprintf("the evidence ledger could not record this export when it was given (%s); %s", r.Reason, later)})
	default:
		return bad(fmt.Errorf("ledger_record: state %q with scope %s", r.State, r.Scope))
	}
	return r
}

// VerifyExport checks an evidence export directory (ADR-007 C7) against the trust anchor (nil: none, and the verdict
// is at best UNKNOWN): every file against SHA256SUMS, SHA256SUMS.sig against SHA256SUMS and the ledger's current key,
// and the ledger with its bundles (§8.2) and head statements: the export's own ledger-head-*.json, then heads. A
// single-bundle export (EXPORT.json scope "bundle") verifies the whole ledger and its one bundle (VerifyLedgerBundle).
// What EXPORT.json states about the export's record is reported in Record: not_recorded is UNKNOWN. An error means the
// verification could not run (no verdict; a four-level tool reports UNKNOWN).
func VerifyExport(dir string, anchor ed25519.PublicKey, heads [][]byte) (ExportReport, error) {
	r := ExportReport{Dir: dir, HeadFiles: []string{}, Exports: []ExportRecord{}}
	if fi, err := os.Stat(dir); err != nil {
		return r, err
	} else if !fi.IsDir() {
		return r, fmt.Errorf("%s: not a directory", dir)
	}
	r.Record = readExportManifest(dir)
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
	ledger, bundles := filepath.Join(dir, ExportLedgerFile), filepath.Join(dir, ExportBundlesDir)
	if r.Record.Scope == ExportScopeBundle { // one bundle with the whole ledger: the other bundles are left out by design
		r.Ledger, err = VerifyLedgerBundle(ledger, bundles, anchor, statements, r.Record.Bundle)
	} else {
		r.Ledger, err = VerifyLedgerHeads(ledger, bundles, anchor, statements)
	}
	if err != nil {
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
			PeriodFrom: sj["period_from"], PeriodTo: sj["period_to"], SumsSHA256: sj["sha256sums_sha256"], Export: sj["export"], CreatedTime: man.Created,
			NotRecordedReason: sj["not_recorded_reason"]})
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
