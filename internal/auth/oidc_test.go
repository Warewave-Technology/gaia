package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// idp is a minimal OpenID provider: discovery, JWKS, and a token endpoint
// that checks the client and PKCE and returns an ID token with the claims
// a test chose for the next sign-in.
type idp struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]grant
}

type grant struct {
	nonce, challenge string
	claims           map[string]any
}

func newIdP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: key, codes: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		if id != "gaia" || secret != "s3cret" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		g, ok := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		p.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		c := map[string]any{"iss": p.srv.URL, "aud": "gaia", "nonce": g.nonce, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		for k, v := range g.claims {
			c[k] = v
		}
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
		tok, err := jwt.Signed(signer).Claims(c).Serialize()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": tok, "expires_in": 3600})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// authorize plays the provider's sign-in page: it takes the redirect Gaia
// sent the browser to and answers with a code for claims.
func (p *idp) authorize(t *testing.T, location string, claims map[string]any) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, p.srv.URL+"/authorize") {
		t.Fatalf("redirect to %s", location)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorization request %v", q)
	}
	code := fmt.Sprintf("code-%d", time.Now().UnixNano())
	p.mu.Lock()
	p.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), claims: claims}
	p.mu.Unlock()
	return q.Get("redirect_uri") + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(q.Get("state"))
}

type secretMap map[string]map[string]string

func (s secretMap) Read(_ context.Context, ref string) (map[string]string, error) {
	if v, ok := s[ref]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("%s: no such secret", ref)
}

func setupOIDC(t *testing.T) (*env, *idp) {
	t.Helper()
	p := newIdP(t)
	g := labGraphWith(t, func(path, c string) string {
		if path != "platform/auth.yaml" {
			return c
		}
		return strings.Replace(c, "  providers:\n", fmt.Sprintf(`  providers:
    - name: corp-sso
      type: oidc
      issuer: %s
      clientId: gaia
      clientSecret: bao://kv/platform/gaia/oidc
      groupsClaim: groups
      mfa: { trustIdp: true, acceptAmr: [hwk], fallback: local-passkey }
`, p.srv.URL), 1)
	})
	e := setupWith(t, g)
	e.auth.WithSecrets(secretMap{"bao://kv/platform/gaia/oidc": {"client_secret": "s3cret"}})
	return e, p
}

// noRedirect is a browser that stops at every redirect, so that the test
// can play the identity provider.
func (e *env) noRedirect(userHandle []byte) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: e.t, base: e.srv.URL,
		c:    &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		auth: virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{UserHandle: userHandle}),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)}
}

func (b *browser) get(url string) *http.Response {
	b.t.Helper()
	resp, err := b.c.Get(url)
	if err != nil {
		b.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

// oidcLogin goes through the provider and returns where Gaia sent the
// browser at the end.
func (b *browser) oidcLogin(p *idp, claims map[string]any) *http.Response {
	b.t.Helper()
	start := b.get(b.base + "/auth/oidc/corp-sso/start?next=/runs")
	if start.StatusCode != http.StatusFound {
		b.t.Fatalf("start: %d", start.StatusCode)
	}
	callback := p.authorize(b.t, start.Header.Get("Location"), claims)
	// The redirect URI is AuthConfig.url's; send it to the test server.
	callback = strings.Replace(callback, "http://localhost:8080", b.base, 1)
	return b.get(callback)
}

func TestOIDCWithMFAShown(t *testing.T) {
	e, p := setupOIDC(t)
	b := e.noRedirect(nil)
	resp := b.oidcLogin(p, map[string]any{"sub": "u-1", "preferred_username": "Ayse", "name": "Ayşe K.", "groups": []string{"/team-platform"}, "amr": []string{"pwd", "hwk"}})
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/runs" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	code, me := b.do("GET", "/api/v1/me", nil)
	if code != 200 || me["username"] != "ayse" || !strings.Contains(strings.Join(anyStrings(me["roles"]), ","), "platform-admin") || me["strongAuthAt"] == nil {
		t.Fatalf("me: %d %v", code, me)
	}
	// Groups follow the provider at every sign-in.
	b2 := e.noRedirect(nil)
	b2.oidcLogin(p, map[string]any{"sub": "u-1", "preferred_username": "ayse", "groups": []string{"team-web"}, "amr": []string{"hwk"}})
	_, me = b2.do("GET", "/api/v1/me", nil)
	if teams := strings.Join(anyStrings(me["teams"]), ","); teams != "team-web" {
		t.Errorf("teams after the second sign-in = %s", teams)
	}
}

func TestOIDCWithoutMFAAsksForGaiasPasskey(t *testing.T) {
	e, p := setupOIDC(t)
	ctx := context.Background()
	b := e.noRedirect(nil)
	resp := b.oidcLogin(p, map[string]any{"sub": "u-2", "preferred_username": "mehmet", "amr": []string{"pwd"}})
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), "/login?step=passkey") {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, _ := b.do("GET", "/api/v1/me", nil); code != 401 {
		t.Fatalf("signed in without the passkey: %d", code)
	}
	u, err := e.store.Q().UserByName(ctx, "mehmet")
	if err != nil {
		t.Fatal(err)
	}
	b.auth = virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{UserHandle: []byte(u.ID)})

	// First sign-in: no passkey yet, so the step registers one.
	code, start := b.do("POST", "/api/v1/auth/oidc/passkey/begin", nil)
	if code != 200 || start["register"] != true {
		t.Fatalf("step begin: %d %v", code, start)
	}
	att, _ := virtualwebauthn.ParseAttestationOptions(options(t, start))
	resp1 := virtualwebauthn.CreateAttestationResponse(rp, b.auth, b.cred, *att)
	b.auth.AddCredential(b.cred)
	code, done := b.do("POST", "/api/v1/auth/oidc/passkey/finish", map[string]any{"flow": start["flow"], "credential": json.RawMessage(resp1)})
	if code != 200 || done["next"] != "/runs" || len(done["recoveryCodes"].([]any)) != recoveryCodes {
		t.Fatalf("step finish: %d %v", code, done)
	}
	if code, _ := b.do("GET", "/api/v1/me", nil); code != 200 {
		t.Errorf("after the passkey: %d", code)
	}

	// Next time the step asks for that passkey.
	b2 := e.noRedirect(nil)
	b2.auth, b2.cred = b.auth, b.cred
	b2.oidcLogin(p, map[string]any{"sub": "u-2", "preferred_username": "mehmet", "amr": []string{"pwd"}})
	code, start = b2.do("POST", "/api/v1/auth/oidc/passkey/begin", nil)
	if code != 200 || start["register"] != false {
		t.Fatalf("second step begin: %d %v", code, start)
	}
	as, _ := virtualwebauthn.ParseAssertionOptions(options(t, start))
	resp2 := virtualwebauthn.CreateAssertionResponse(rp, b2.auth, b2.cred, *as)
	if code, out := b2.do("POST", "/api/v1/auth/oidc/passkey/finish", map[string]any{"flow": start["flow"], "credential": json.RawMessage(resp2)}); code != 200 {
		t.Fatalf("second step finish: %d %v", code, out)
	}
	// Without a pending OIDC sign-in the step does nothing.
	if code, _ := e.noRedirect(nil).do("POST", "/api/v1/auth/oidc/passkey/begin", nil); code != 401 {
		t.Errorf("step without OIDC: %d", code)
	}
}

func TestOIDCRefusals(t *testing.T) {
	e, p := setupOIDC(t)
	ctx := context.Background()
	b := e.noRedirect(nil)
	if resp := b.get(e.srv.URL + "/auth/oidc/corp-sso/callback?code=x&state=forged"); resp.StatusCode != 400 {
		t.Errorf("forged state: %d", resp.StatusCode)
	}
	if resp := b.get(e.srv.URL + "/auth/oidc/nope/start"); resp.StatusCode != 400 {
		t.Errorf("unknown provider: %d", resp.StatusCode)
	}
	// A local account keeps its name; the OIDC account gets another.
	e.user("can")
	b.oidcLogin(p, map[string]any{"sub": "u-3", "preferred_username": "can", "amr": []string{"hwk"}})
	if _, err := e.store.Q().UserByName(ctx, "can.corp-sso"); err != nil {
		t.Errorf("the OIDC account's name: %v", err)
	}
	// A disabled account is refused at the callback.
	u, _ := e.store.Q().UserByName(ctx, "can.corp-sso")
	if _, err := e.store.Pool().Exec(ctx, "update users set enabled = false where id = $1", u.ID); err != nil {
		t.Fatal(err)
	}
	if resp := e.noRedirect(nil).oidcLogin(p, map[string]any{"sub": "u-3", "amr": []string{"hwk"}}); resp.StatusCode != 403 {
		t.Errorf("disabled account: %d", resp.StatusCode)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/runs": "/runs", "": "/", "https://evil.example": "/", "//evil.example": "/", "/\\evil": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q", in, got)
		}
	}
}
