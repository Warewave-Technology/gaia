package auth

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B, SHA-1, truncated to 6 digits.
func TestTOTPVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"} {
		if got := TOTPCode(secret, unix/TOTPStep); got != want {
			t.Errorf("T=%d: %s, want %s", unix, got, want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := NewTOTPSecret()
	now := time.Unix(1_800_000_000, 0)
	cur := TOTPStepAt(now)
	for _, d := range []int64{-1, 0, 1} {
		if s, ok := VerifyTOTP(secret, TOTPCode(secret, cur+d), now, 0); !ok || s != cur+d {
			t.Errorf("step %+d: %d %v", d, s, ok)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := VerifyTOTP(secret, TOTPCode(secret, cur+d), now, 0); ok {
			t.Errorf("step %+d accepted", d)
		}
	}
	if _, ok := VerifyTOTP(secret, TOTPCode(secret, cur), now, cur); ok {
		t.Error("a used code was accepted again")
	}
	if _, ok := VerifyTOTP(secret, TOTPCode(secret, cur-1), now, cur); ok {
		t.Error("a code older than the last used one was accepted")
	}
	if _, ok := VerifyTOTP(secret, "12345", now, 0); ok {
		t.Error("5 digits accepted")
	}
	if !strings.HasPrefix(TOTPURI(secret, "Gaia gaia-01", "yigit"), "otpauth://totp/Gaia%20gaia-01:yigit?") {
		t.Error(TOTPURI(secret, "Gaia gaia-01", "yigit"))
	}
}

func TestBlocklist(t *testing.T) {
	n, err := BlocklistSize()
	if err != nil || n < 10000 {
		t.Fatalf("%d entries, %v", n, err)
	}
	for p, want := range map[string]bool{
		"1qaz2wsx3edc4rfv":       true, // on the list
		"1QAZ2WSX3EDC4RFV":       true, // case does not matter
		"aaaaaaaaaaaaaaaaaaaa":   true, // too few characters
		"abababababababab":       true,
		"correct horse battery9": false,
	} {
		if got, err := Blocklisted(p); err != nil || got != want {
			t.Errorf("%q: %v %v, want %v", p, got, err, want)
		}
	}
}

func TestCheckPassword(t *testing.T) {
	broken := func(p, user, inst string) string {
		r, err := CheckPassword(p, user, inst)
		if err != nil {
			t.Fatal(err)
		}
		var bad []string
		for _, x := range r {
			if !x.OK {
				bad = append(bad, x.Rule)
			}
		}
		return strings.Join(bad, ",")
	}
	for _, c := range []struct{ p, user, inst, want string }{
		{"short but fine", "yigit", "gaia-01", "length"},
		{strings.Repeat("abcdefghij", 13), "yigit", "gaia-01", "length"},
		{"fifteen chars!!", "yigit", "gaia-01", ""},
		{"1qaz2wsx3edc4rfv", "yigit", "gaia-01", "blocklist"},
		{"my name is Yigit, ok", "yigit", "gaia-01", "no_username"},
		{"this is the GAIA server", "yigit", "lab", "no_instance"},
		{"backup-host-lab0 secret", "yigit", "backup-host-lab0.example", "no_instance"},
		{"çok uzun türkçe şifre", "yigit", "x", ""}, // characters, not bytes
	} {
		if got := broken(c.p, c.user, c.inst); got != c.want {
			t.Errorf("%q: broken %q, want %q", c.p, got, c.want)
		}
	}
	r, _ := CheckPassword("short", "u", "i")
	if err := Broken(r); err == nil || !strings.Contains(err.Error(), "15 to 128 characters (5 now)") {
		t.Fatal(err)
	}
}

func TestRecoveryCodes(t *testing.T) {
	cs := NewRecoveryCodes()
	seen := map[string]bool{}
	for _, c := range cs {
		if len(c) != 14 || seen[c] {
			t.Fatalf("code %q", c)
		}
		seen[c] = true
	}
	if len(cs) != 10 || HashRecoveryCode(cs[0]) != HashRecoveryCode(" "+strings.ToUpper(strings.ReplaceAll(cs[0], "-", ""))+" ") {
		t.Fatal("hash depends on how the code is typed")
	}
}

func TestBreached(t *testing.T) {
	sum := sha1.Sum([]byte("a breached password"))
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/range/"+h[:5] || r.Header.Get("Add-Padding") != "true" {
			http.Error(w, "bad", 400)
			return
		}
		fmt.Fprintf(w, "0000000000000000000000000000000000A:0\r\n%s:42\r\n", h[5:])
	}))
	defer srv.Close()
	if got, err := Breached(context.Background(), srv.Client(), srv.URL+"/range/", "a breached password"); err != nil || !got {
		t.Fatal(got, err)
	}
	if _, err := Breached(context.Background(), srv.Client(), srv.URL+"/range/", "another password"); err == nil {
		t.Fatal("an answer that is not 200 passed the check") // its prefix is different: 400
	}
}
