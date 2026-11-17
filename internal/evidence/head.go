package evidence

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Head statements (specification §6.8, §8.6): a signed record of a ledger's last entry, kept outside the ledger (Gaia
// puts it in its daily report). A verifier given one detects a ledger cut off, or rewritten, after it (§11.1).

// HeadFormatV1 is the format of a head statement.
const HeadFormatV1 = "restore-evidence-head"

// Finding codes of head statements (§8.3).
const (
	CodeHeadMalformed        = "head_malformed"
	CodeHeadSignatureInvalid = "head_signature_invalid"
	CodeHeadMismatch         = "head_mismatch"
	CodeLedgerTruncated      = "ledger_truncated"
	CodeHeadUnverifiable     = "head_unverifiable"
)

// Head is what a head statement says.
type Head struct {
	Time        string `json:"time"`
	KeyID       string `json:"key_id"`
	Seq         int    `json:"seq"`
	EntrySHA256 string `json:"entry_sha256"`
}

type headPayload struct {
	Format        string `json:"format"`
	FormatVersion int    `json:"format_version"`
	Head
}

// HeadCheck is the outcome of checking one head statement against a ledger.
type HeadCheck struct {
	Head
	Result string `json:"result"` // "OK" or the finding code
	Detail string `json:"detail,omitempty"`
}

// HeadStatement signs a statement of the ledger's last entry with key, which must be the ledger's current key. It reads
// the ledger under the same lock as Append, and refuses a ledger that does not verify (without bundles). The statement
// is one JSON object, as a ledger line (§6.2), without a line feed.
func HeadStatement(ledger string, key ed25519.PrivateKey, t time.Time) (Head, []byte, error) {
	f, s, err := openLedger(ledger)
	if err != nil {
		return Head{}, nil, err
	}
	defer f.Close()
	if s == nil {
		return Head{}, nil, fmt.Errorf("ledger %s is empty: it has no head", ledger)
	}
	pub := key.Public().(ed25519.PublicKey)
	if !s.current.Equal(pub) {
		return Head{}, nil, fmt.Errorf("ledger %s: the current key is %s, not %s", ledger, KeyID(s.current), KeyID(pub))
	}
	h := Head{Time: t.UTC().Format(time.RFC3339Nano), KeyID: KeyID(pub), Seq: s.lines, EntrySHA256: s.last}
	b, _, err := signLine(headPayload{Format: HeadFormatV1, FormatVersion: FormatVersion, Head: h}, key)
	if err != nil {
		return Head{}, nil, err
	}
	return h, b[:len(b)-1], nil
}

// parseHead reads a head statement (§8.6 steps H1–H3). The code is set when it fails.
func parseHead(b []byte) (Head, []byte, []byte, string, error) {
	var h Head
	bad := func(err error) (Head, []byte, []byte, string, error) { return h, nil, nil, CodeHeadMalformed, err }
	w, err := parseObject(b)
	if err != nil {
		return bad(err)
	}
	payload, ok := w["payload"].(string)
	if !ok {
		return bad(errors.New(`"payload": missing or not a string`))
	}
	sigText, ok := w["sig"].(string)
	if !ok {
		return bad(errors.New(`"sig": missing or not a string`))
	}
	if err := surrogates(b); err != nil {
		return bad(err)
	}
	pb := []byte(payload)
	p, err := parseObject(pb)
	if err != nil {
		return bad(fmt.Errorf("payload: %w", err))
	}
	f, err := str(p, "format")
	if err == nil && f != HeadFormatV1 {
		err = fmt.Errorf("format %q", f)
	}
	if err != nil {
		return bad(err)
	}
	v, err := integer(p, "format_version")
	if err != nil {
		return bad(err)
	}
	if v != FormatVersion {
		return h, nil, nil, CodeFormatUnsupported, fmt.Errorf("format_version %d", v)
	}
	if h.Time, err = str(p, "time"); err == nil && !validTime(h.Time) {
		err = fmt.Errorf("time %q: not an RFC 3339 UTC time", h.Time)
	}
	if err != nil {
		return bad(err)
	}
	if h.KeyID, err = hex64(p, "key_id"); err != nil {
		return bad(err)
	}
	seq, err := integer(p, "seq")
	if err == nil && seq < 1 {
		err = errors.New(`"seq": less than 1`)
	}
	if err != nil {
		return bad(err)
	}
	h.Seq = int(seq)
	if h.EntrySHA256, err = hex64(p, "entry_sha256"); err != nil {
		return bad(err)
	}
	return h, pb, []byte(sigText), "", nil
}

// checkHead is §8.6 step H4 for one statement against what L2 read (s nil: L2 did not run; anchor, if given, is
// still a known key).
func checkHead(b []byte, s *ledgerScan, anchor ed25519.PublicKey) HeadCheck {
	h, pb, sigText, code, err := parseHead(b)
	if err != nil {
		return HeadCheck{Head: h, Result: code, Detail: err.Error()}
	}
	sig, sigOK := decodeSig64(string(sigText))
	signed := func(k ed25519.PublicKey) bool { return sigOK && KeyID(k) == h.KeyID && ed25519.Verify(k, pb, sig) }
	n := 0
	known := map[string]ed25519.PublicKey{}
	if anchor != nil {
		known[KeyID(anchor)] = anchor
	}
	if s != nil {
		n = len(s.hashes)
		for id, k := range s.known {
			known[id] = k
		}
	}
	c := HeadCheck{Head: h, Result: OK}
	switch {
	case h.Seq <= n:
		k := s.after[h.Seq-1]
		switch {
		case !signed(k):
			c.Result, c.Detail = CodeHeadSignatureInvalid, fmt.Sprintf("not signed by %s, the key current after entry %d", KeyID(k), h.Seq)
		case s.hashes[h.Seq-1] != h.EntrySHA256:
			c.Result, c.Detail = CodeHeadMismatch, fmt.Sprintf("entry %d hashes to %s; the head statement of %s recorded %s", h.Seq, s.hashes[h.Seq-1], h.Time, h.EntrySHA256)
		}
	default:
		k, ok := known[h.KeyID]
		switch {
		case !ok:
			c.Result, c.Detail = CodeHeadUnverifiable, fmt.Sprintf("the head statement names entry %d of a ledger with %d, signed by key %s, which this ledger does not know", h.Seq, n, h.KeyID)
		case !signed(k):
			c.Result, c.Detail = CodeHeadSignatureInvalid, fmt.Sprintf("not signed by %s", h.KeyID)
		default:
			c.Result, c.Detail = CodeLedgerTruncated, fmt.Sprintf("the head statement of %s records entry %d; the ledger has %d", h.Time, h.Seq, n)
		}
	}
	return c
}

// checkHeads adds the head statements' findings to r.
func (r *LedgerReport) checkHeads(heads [][]byte, s *ledgerScan, anchor ed25519.PublicKey) {
	for i, b := range heads {
		c := checkHead(b, s, anchor)
		r.Heads = append(r.Heads, c)
		if c.Result != OK {
			r.add(c.Result, fmt.Sprintf("head %d", i+1), c.Detail)
		}
	}
}

// HeadFromDocument finds the head statement in a document: a head statement itself, or a JSON object whose member
// "ledger_head" is one (Gaia's daily report). It returns the statement's bytes for VerifyLedgerHeads.
func HeadFromDocument(b []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if lh, ok := doc["ledger_head"]; ok {
		return lh, nil
	}
	if _, ok := doc["payload"]; ok {
		return b, nil
	}
	return nil, errors.New(`neither a head statement nor a document with "ledger_head"`)
}
