package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (ADR-008 §3): RFC 6238 with HMAC-SHA1, a 30 s step and 6 digits, what every authenticator app reads from an
// otpauth:// URI. A code is accepted one step early or late, and never twice: the store keeps the last step used.
const (
	TOTPStep   = 30
	TOTPDigits = 6
	totpSkew   = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a new 160-bit secret (RFC 4226 §4 recommends 160 bits).
func NewTOTPSecret() []byte {
	b := make([]byte, 20)
	rand.Read(b) // crypto/rand.Read never fails (Go 1.24)
	return b
}

// TOTPSecretText is the secret as an authenticator app takes it typed in.
func TOTPSecretText(secret []byte) string { return b32.EncodeToString(secret) }

// ParseTOTPSecret reads a secret written by TOTPSecretText.
func ParseTOTPSecret(s string) ([]byte, error) { return b32.DecodeString(s) }

// TOTPURI is the otpauth:// URI an authenticator app reads from a QR code.
func TOTPURI(secret []byte, issuer, account string) string {
	q := url.Values{}
	q.Set("secret", TOTPSecretText(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(TOTPStep))
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}

// TOTPStepAt is the time step of t.
func TOTPStepAt(t time.Time) int64 { return t.Unix() / TOTPStep }

// TOTPCode is the code of a step (RFC 4226 §5.3, dynamic truncation).
func TOTPCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// VerifyTOTP checks a code at now. It returns the step the code belongs to, or false when it matches no step within
// ±1 of now, or only a step at or before lastUsed (a code is used once).
func VerifyTOTP(secret []byte, code string, now time.Time, lastUsed int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, false
	}
	cur := TOTPStepAt(now)
	found, ok := int64(0), false
	for s := cur - totpSkew; s <= cur+totpSkew; s++ { // every step is compared: the time taken does not tell which matched
		if subtle.ConstantTimeCompare([]byte(TOTPCode(secret, s)), []byte(code)) == 1 && s > lastUsed {
			found, ok = s, true
		}
	}
	return found, ok
}

// Recovery codes (ADR-008 §3): 10 per user, single use, stored hashed. Each has 60 random bits, written in three
// groups of four base32 letters and digits.
const RecoveryCodes = 10

// NewRecoveryCodes returns RecoveryCodes new codes.
func NewRecoveryCodes() []string {
	out := make([]string, RecoveryCodes)
	for i := range out {
		b := make([]byte, 8)
		rand.Read(b)
		s := strings.ToLower(b32.EncodeToString(b))[:12]
		out[i] = s[0:4] + "-" + s[4:8] + "-" + s[8:12]
	}
	return out
}

// HashRecoveryCode is the stored form of a code, the same however it is typed (case, dashes, spaces).
func HashRecoveryCode(code string) string {
	c := strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	return SHA256Hex(c)
}

// SHA256Hex is the hex sha256 of s: the stored form of a session or API token (only the hash is stored, §5, §9).
func SHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// NewToken returns an opaque 256-bit token with a prefix that tells its kind (gaia_ a session, gaiat_ an API token).
func NewToken(prefix string) string {
	b := make([]byte, 32)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}
