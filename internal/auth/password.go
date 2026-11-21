// Package auth holds the parts of Gaia's sign-in (ADR-008) that need no database or HTTP: the password rules and the
// embedded blocklist (§6), TOTP (§3, RFC 6238), recovery codes (§3), opaque tokens (§5, §9) and the k-anonymity
// breach check (§6, off by default).
package auth

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// Password rules (ADR-008 §6). Fixed: there is no setting for them.
const (
	MinPasswordLength = 15
	MaxPasswordLength = 128
	PasswordHistory   = 5 // a new password is not one of the user's last 5
)

// The blocklist: common and breached passwords of 15 characters or more from a pinned public list, lowercased
// (scripts/build-blocklist.sh writes the file and prints the sha256 pinned here).
//
//go:embed blocklist.txt.gz
var blocklistGz []byte

const blocklistSHA256 = "cbdc28a68ea4bc20d214755fb333d816c55ceada6cc3da3bd9b70ecea42aead0"

// BlocklistVersion names the embedded list: a user whose password was set against another version is checked again at
// the next sign-in (§6: a password found on the list after an update must be changed).
const BlocklistVersion = "seclists-xato-1m@c205c36:" + blocklistSHA256

var blocklist = sync.OnceValues(func() (map[string]struct{}, error) {
	zr, err := gzip.NewReader(bytes.NewReader(blocklistGz))
	if err != nil {
		return nil, fmt.Errorf("password blocklist: %w", err)
	}
	h := sha256.New()
	set := map[string]struct{}{}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		h.Write(sc.Bytes())
		h.Write([]byte{'\n'})
		set[sc.Text()] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("password blocklist: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != blocklistSHA256 {
		return nil, fmt.Errorf("password blocklist: sha256 %s, want %s", got, blocklistSHA256)
	}
	return set, nil
})

// BlocklistSize is the number of entries in the embedded list.
func BlocklistSize() (int, error) {
	set, err := blocklist()
	return len(set), err
}

// Blocklisted reports whether p is on the embedded list, or is as weak as what is (fewer than 5 different
// characters: "aaaaaaaaaaaaaaa", "abababababababab").
func Blocklisted(p string) (bool, error) {
	set, err := blocklist()
	if err != nil {
		return false, err
	}
	if _, ok := set[strings.ToLower(p)]; ok {
		return true, nil
	}
	seen := map[rune]bool{}
	for _, r := range strings.ToLower(p) {
		seen[r] = true
	}
	return len(seen) < 5, nil
}

// ErrWeakPassword is the error of a password that breaks a rule; the message names each rule broken.
var ErrWeakPassword = errors.New("the password breaks a rule")

// Rule is one password rule and whether a password keeps it (the sign-in screens list them, ADR-008 §6).
type Rule struct {
	Rule string `json:"rule"` // length, blocklist, no_username, no_instance, history
	OK   bool   `json:"ok"`
	Text string `json:"text"`
}

// CheckPassword checks a new password against the rules that need no history: its length, the blocklist, and that it
// does not contain the user name, the instance name or "gaia". The history rule is the store's (it has the hashes).
func CheckPassword(p, username, instance string) ([]Rule, error) {
	n := utf8.RuneCountInString(p)
	listed, err := Blocklisted(p)
	if err != nil {
		return nil, err
	}
	lp := strings.ToLower(p)
	has := func(s string) bool { s = strings.ToLower(s); return len(s) >= 3 && strings.Contains(lp, s) }
	short := strings.SplitN(strings.ToLower(instance), ".", 2)[0] // gaia-01 of gaia-01.prod.example
	rules := []Rule{
		{"length", n >= MinPasswordLength && n <= MaxPasswordLength, fmt.Sprintf("%d to %d characters (%d now)", MinPasswordLength, MaxPasswordLength, n)},
		{"blocklist", !listed, "not in the list of breached and common passwords"},
		{"no_username", !has(username), "does not contain your user name"},
		{"no_instance", !has("gaia") && !has(instance) && !has(short), `does not contain "gaia" or this instance's name`},
	}
	return rules, nil
}

// Broken returns an ErrWeakPassword naming the rules not kept, or nil when all are.
func Broken(rules []Rule) error {
	var bad []string
	for _, r := range rules {
		if !r.OK {
			bad = append(bad, r.Text)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrWeakPassword, strings.Join(bad, "; "))
}
