package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/descope/virtualwebauthn"
	"golang.org/x/crypto/ssh"
)

// bearer calls the API with a CLI access token.
func (e *env) bearer(method, path, token string) int {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func pkce() (verifier, challenge string) {
	verifier = "a-verifier-that-is-long-enough-for-pkce-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestCLILoopbackLogin(t *testing.T) {
	e := setup(t)
	id, link := e.user("ayse", "team-platform")
	b := e.browser(id)
	b.enroll(link)
	verifier, challenge := pkce()
	redirect := "http://127.0.0.1:53682/callback"

	code, out := b.do("POST", "/api/v1/auth/cli/authorize", map[string]string{"redirectUri": redirect, "state": "st", "codeChallenge": challenge})
	if code != 200 {
		t.Fatalf("authorize: %d %v", code, out)
	}
	u, _ := url.Parse(out["redirect"].(string))
	if u.Host != "127.0.0.1:53682" || u.Query().Get("state") != "st" || u.Query().Get("code") == "" {
		t.Fatalf("redirect = %s", u)
	}
	grant := u.Query().Get("code")
	if code, _ := b.do("POST", "/api/v1/auth/cli/token", map[string]string{"code": grant, "codeVerifier": "wrong", "redirectUri": redirect}); code != 401 {
		t.Errorf("wrong verifier: %d", code)
	}
	// The code was used up by the failed attempt; get another.
	_, out = b.do("POST", "/api/v1/auth/cli/authorize", map[string]string{"redirectUri": redirect, "state": "st", "codeChallenge": challenge})
	u, _ = url.Parse(out["redirect"].(string))
	grant = u.Query().Get("code")
	code, tok := b.do("POST", "/api/v1/auth/cli/token", map[string]string{"code": grant, "codeVerifier": verifier, "redirectUri": redirect})
	if code != 200 {
		t.Fatalf("token: %d %v", code, tok)
	}
	access, refresh := tok["accessToken"].(string), tok["refreshToken"].(string)
	if e.bearer("GET", "/api/v1/me", access) != 200 {
		t.Error("the access token does not work")
	}
	if code, _ := b.do("POST", "/api/v1/auth/cli/token", map[string]string{"code": grant, "codeVerifier": verifier, "redirectUri": redirect}); code != 401 {
		t.Errorf("a code used twice: %d", code)
	}
	for _, bad := range []string{"https://127.0.0.1:1/x", "http://evil.example:80/x", "http://127.0.0.1/x"} {
		if code, _ := b.do("POST", "/api/v1/auth/cli/authorize", map[string]string{"redirectUri": bad, "state": "s", "codeChallenge": challenge}); code != 400 {
			t.Errorf("redirect %s: %d", bad, code)
		}
	}

	// Refresh mints a new access token; logout ends both.
	code, ref := b.do("POST", "/api/v1/auth/cli/refresh", map[string]string{"refreshToken": refresh})
	if code != 200 || ref["accessToken"] == access {
		t.Fatalf("refresh: %d %v", code, ref)
	}
	newAccess := ref["accessToken"].(string)
	if code, _ := b.do("POST", "/api/v1/auth/cli/logout", map[string]string{"refreshToken": refresh}); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	for _, tk := range []string{access, newAccess} {
		if e.bearer("GET", "/api/v1/me", tk) != 401 {
			t.Error("an access token outlived its CLI session")
		}
	}
	if code, _ := b.do("POST", "/api/v1/auth/cli/refresh", map[string]string{"refreshToken": refresh}); code != 401 {
		t.Errorf("refresh after logout: %d", code)
	}
}

func TestDeviceLogin(t *testing.T) {
	e := setup(t)
	id, link := e.user("ops")
	b := e.browser(id)
	b.enroll(link)
	cli := e.browser("")

	code, d := cli.do("POST", "/api/v1/auth/device", nil)
	if code != 200 || !strings.HasSuffix(d["verificationUri"].(string), "/device") {
		t.Fatalf("device: %d %v", code, d)
	}
	device, user := d["deviceCode"].(string), d["userCode"].(string)
	poll := func() (int, map[string]any) {
		return cli.do("POST", "/api/v1/auth/device/token", map[string]string{"deviceCode": device})
	}
	if code, out := poll(); code != 400 || out["error"] != "authorization_pending" {
		t.Errorf("first poll: %d %v", code, out)
	}
	if code, out := poll(); code != 400 || out["error"] != "slow_down" {
		t.Errorf("fast poll: %d %v", code, out)
	}
	// The person sees where the request came from, then approves it.
	code, info := b.do("GET", "/api/v1/auth/device/"+strings.ToLower(strings.ReplaceAll(user, "-", "")), nil)
	if code != 200 || info["ip"] != "127.0.0.1" {
		t.Fatalf("info: %d %v", code, info)
	}
	if code, _ := b.do("POST", "/api/v1/auth/device/"+user+"/approve", nil); code != 204 {
		t.Fatalf("approve: %d", code)
	}
	code, tok := poll()
	if code != 200 || e.bearer("GET", "/api/v1/me", tok["accessToken"].(string)) != 200 {
		t.Fatalf("token: %d %v", code, tok)
	}
	if code, out := poll(); code != 400 || out["error"] != "invalid_grant" {
		t.Errorf("poll after use: %d %v", code, out)
	}

	// A denied code ends the CLI's wait.
	_, d = cli.do("POST", "/api/v1/auth/device", nil)
	if code, _ := b.do("POST", "/api/v1/auth/device/"+d["userCode"].(string)+"/deny", nil); code != 204 {
		t.Fatalf("deny: %d", code)
	}
	if code, out := cli.do("POST", "/api/v1/auth/device/token", map[string]string{"deviceCode": d["deviceCode"].(string)}); code != 400 || out["error"] != "access_denied" {
		t.Errorf("denied: %d %v", code, out)
	}
	// A CLI token cannot approve device or CLI sign-ins.
	if code := e.bearer("POST", "/api/v1/auth/device/ABCD-EFGH/approve", tok["accessToken"].(string)); code != 403 {
		t.Errorf("approve with a CLI token: %d", code)
	}
}

func TestStepUpAndSigningKeys(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, link := e.user("selin")
	b := e.browser(id)
	b.enroll(link)

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	sshKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " selin@laptop"

	// Fifteen minutes on, adding a key needs the passkey again.
	later := time.Now().Add(15 * time.Minute)
	e.auth.now = func() time.Time { return later }
	if _, err := e.store.Pool().Exec(ctx, "update sessions set last_seen_at = now() + interval '15 minutes'"); err != nil {
		t.Fatal(err)
	}
	if code, out := b.do("POST", "/api/v1/me/signing-keys", map[string]string{"name": "laptop", "publicKey": sshKey}); code != 403 || out["stepUp"] != true {
		t.Fatalf("add without step-up: %d %v", code, out)
	}
	code, start := b.do("POST", "/api/v1/auth/step-up/begin", nil)
	if code != 200 {
		t.Fatalf("step-up begin: %d %v", code, start)
	}
	opts, _ := virtualwebauthn.ParseAssertionOptions(options(t, start))
	resp := virtualwebauthn.CreateAssertionResponse(rp, b.auth, b.cred, *opts)
	if code, out := b.do("POST", "/api/v1/auth/step-up/finish", map[string]any{"flow": start["flow"], "credential": json.RawMessage(resp)}); code != 204 {
		t.Fatalf("step-up finish: %d %v", code, out)
	}
	// SetStrongAuth used the database clock; follow it.
	e.auth.now = time.Now
	code, k := b.do("POST", "/api/v1/me/signing-keys", map[string]string{"name": "laptop", "publicKey": sshKey})
	if code != 201 || !strings.HasPrefix(k["fingerprint"].(string), "SHA256:") || k["kind"] != "ssh" {
		t.Fatalf("add ssh key: %d %v", code, k)
	}
	if code, _ := b.do("POST", "/api/v1/me/signing-keys", map[string]string{"publicKey": sshKey}); code != 400 {
		t.Errorf("the same key twice: %d", code)
	}
	if code, _ := b.do("POST", "/api/v1/me/signing-keys", map[string]string{"publicKey": "not a key"}); code != 400 {
		t.Errorf("garbage key: %d", code)
	}

	ent, err := openpgp.NewEntity("Selin", "", "selin@gaia.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	_ = ent.Serialize(w)
	_ = w.Close()
	code, g := b.do("POST", "/api/v1/me/signing-keys", map[string]string{"name": "gpg", "publicKey": buf.String()})
	if code != 201 || g["kind"] != "gpg" || len(g["fingerprint"].(string)) != 40 || g["fingerprint"] != strings.ToUpper(g["fingerprint"].(string)) {
		t.Fatalf("add gpg key: %d %v", code, g)
	}
	signers, err := e.auth.Signers(ctx)
	if err != nil || len(signers) != 2 || signers[0].Username != "selin" {
		t.Errorf("signers = %+v, %v", signers, err)
	}
	if code, _ := b.do("DELETE", "/api/v1/me/signing-keys/"+url.PathEscape(k["fingerprint"].(string)), nil); code != 204 {
		t.Errorf("revoke: %d", code)
	}
	if signers, _ := e.auth.Signers(ctx); len(signers) != 1 {
		t.Errorf("signers after revoke = %+v", signers)
	}
}
