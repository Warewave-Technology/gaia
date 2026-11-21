package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// mockIdP is an OIDC provider for tests: discovery, keys, the code flow with PKCE, refresh, the device grant. The
// test plays the user: it "signs in" by asking the IdP for a code for one of its users.
type mockIdP struct {
	t       *testing.T
	srv     *httptest.Server
	key     *rsa.PrivateKey
	now     func() time.Time
	mu      sync.Mutex
	users   map[string]*idpUser
	codes   map[string]idpGrant // code -> grant
	refresh map[string]string   // refresh token -> user
	devices map[string]string   // device code -> user ("" = not yet approved)
	down    bool
}

type idpUser struct {
	sub, name string
	groups    []string
	acr       string
	disabled  bool
}

type idpGrant struct {
	user, nonce, challenge string
	authTime               time.Time
}

func newMockIdP(t *testing.T, now func() time.Time) *mockIdP {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockIdP{t: t, key: k, now: now, users: map[string]*idpUser{}, codes: map[string]idpGrant{}, refresh: map[string]string{}, devices: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		u := m.srv.URL
		json.NewEncoder(w).Encode(map[string]any{"issuer": u, "authorization_endpoint": u + "/auth", "token_endpoint": u + "/token",
			"jwks_uri": u + "/jwks", "device_authorization_endpoint": u + "/device", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		e := big.NewInt(int64(k.E)).Bytes()
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64.EncodeToString(k.N.Bytes()), "e": b64.EncodeToString(e)}}})
	})
	mux.HandleFunc("/token", m.token)
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code_challenge_method") != "S256" || r.Form.Get("code_challenge") == "" {
			http.Error(w, `{"error":"invalid_request"}`, 400)
			return
		}
		if r.Form.Get("client_secret") != "idp-client-secret" {
			http.Error(w, `{"error":"invalid_client"}`, 401)
			return
		}
		dc := "dc-" + fmt.Sprint(len(m.devices))
		m.mu.Lock()
		m.devices[dc] = ""
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"device_code": dc, "user_code": "WDJB-MJHT", "verification_uri": m.srv.URL + "/device-ui",
			"expires_in": 600, "interval": 1})
	})
	m.srv = httptest.NewTLSServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockIdP) caPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.srv.Certificate().Raw}))
}

func (m *mockIdP) idToken(u *idpUser, nonce string, authTime time.Time) string {
	now := m.now()
	claims := map[string]any{"iss": m.srv.URL, "sub": u.sub, "aud": "gaia", "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"auth_time": authTime.Unix(), "preferred_username": u.name, "groups": u.groups, "acr": u.acr}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	p, _ := json.Marshal(claims)
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	d := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, d[:])
	if err != nil {
		m.t.Fatal(err)
	}
	return in + "." + b64.EncodeToString(sig)
}

func (m *mockIdP) token(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json") // text/plain would be read as a form
	r.ParseForm()
	user, pass, ok := r.BasicAuth()
	if !ok {
		user, pass = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	if user != "gaia" || pass != "idp-client-secret" {
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
		return
	}
	grant := func(name, nonce string, at time.Time) {
		u := m.users[name]
		rt := "rt-" + name + "-" + fmt.Sprint(len(m.refresh))
		m.refresh[rt] = name
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "refresh_token": rt,
			"id_token": m.idToken(u, nonce, at)})
	}
	bad := func(code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		g, ok := m.codes[r.Form.Get("code")]
		delete(m.codes, r.Form.Get("code"))
		h := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || b64.EncodeToString(h[:]) != g.challenge {
			bad("invalid_grant") // PKCE: the verifier must match the challenge of the authorization request
			return
		}
		grant(g.user, g.nonce, g.authTime)
	case "refresh_token":
		name, ok := m.refresh[r.Form.Get("refresh_token")]
		if !ok || m.users[name].disabled {
			bad("invalid_grant")
			return
		}
		grant(name, "", m.now().Add(-time.Hour))
	case "urn:ietf:params:oauth:grant-type:device_code":
		if r.Form.Get("code_verifier") == "" {
			bad("invalid_request") // the device request had a code_challenge
			return
		}
		name, ok := m.devices[r.Form.Get("device_code")]
		switch {
		case !ok:
			bad("expired_token")
		case name == "":
			bad("authorization_pending")
		default:
			grant(name, "", m.now())
		}
	default:
		bad("unsupported_grant_type")
	}
}

// signIn plays the user at the IdP: given Gaia's redirect to /auth, it returns the callback URL with a code.
func (m *mockIdP) signIn(authURL, user string, authTime time.Time) string {
	u, err := url.Parse(authURL)
	if err != nil || !strings.HasPrefix(authURL, m.srv.URL+"/auth") {
		m.t.Fatalf("not a redirect to the IdP: %q", authURL)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		m.t.Fatalf("the authorization request lacks PKCE, nonce or state: %s", authURL)
	}
	code := "code-" + fmt.Sprint(len(m.codes)) + "-" + user
	m.mu.Lock()
	m.codes[code] = idpGrant{user: user, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), authTime: authTime}
	m.mu.Unlock()
	return q.Get("redirect_uri") + "?code=" + url.QueryEscape(code) + "&state=" + url.QueryEscape(q.Get("state"))
}

// browser is a client for a TLS Gaia that keeps cookies and does not follow redirects.
type browser struct {
	t  *testing.T
	cl *http.Client
	ts *httptest.Server
}

func newBrowser(t *testing.T, ts *httptest.Server) *browser {
	jar, _ := cookiejar.New(nil)
	c := *ts.Client() // ts.Client is shared: each browser has its own jar
	cl := &c
	cl.Jar = jar
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &browser{t: t, cl: cl, ts: ts}
}

func (b *browser) get(path string, h map[string]string) (*http.Response, []byte) {
	b.t.Helper()
	req, _ := http.NewRequest("GET", b.ts.URL+path, nil)
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := b.cl.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp, body
}

func (b *browser) post(path, body string, h map[string]string, out any) int {
	b.t.Helper()
	req, _ := http.NewRequest("POST", b.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	resp, err := b.cl.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// sso signs a user in through the IdP and returns the browser's session (in its cookie jar).
func (m *mockIdP) sso(b *browser, user string) (*http.Response, string) {
	m.t.Helper()
	resp, _ := b.get("/api/v1/oidc/start?next=/vms", nil)
	if resp.StatusCode != http.StatusFound {
		m.t.Fatalf("start: %d", resp.StatusCode)
	}
	cb := m.signIn(resp.Header.Get("Location"), user, m.now())
	u, _ := url.Parse(cb)
	resp, _ = b.get(u.Path+"?"+u.RawQuery, nil)
	return resp, resp.Header.Get("Location")
}

func TestADR008OIDC(t *testing.T) {
	f := newFixture(t)
	f.seed()
	clk := f.clock()
	ctx := context.Background()
	idp := newMockIdP(t, clk.now)
	for _, u := range []*idpUser{
		{sub: "s-admin", name: "ayla", groups: []string{"gaia-admins", "gaia-operators"}, acr: "mfa"},
		{sub: "s-op", name: "omer", groups: []string{"/gaia-operators"}, acr: "mfa"},
		{sub: "s-aud", name: "irem", groups: []string{"internal-audit"}, acr: "mfa"},
		{sub: "s-none", name: "nobody", groups: []string{"staff"}, acr: "mfa"},
		{sub: "s-1fa", name: "weak", groups: []string{"gaia-operators"}, acr: "1"},
	} {
		idp.users[u.name] = u
	}
	ts := httptest.NewTLSServer(f.s.Handler())
	t.Cleanup(ts.Close)
	admin := f.tokens[store.RoleAdmin]
	set := OIDCSettings{Enabled: true, Name: "Lab SSO", Issuer: idp.srv.URL, ClientID: "gaia", GroupsClaim: "groups", CAPEM: idp.caPEM(),
		Roles:        map[string]string{"gaia-admins": "admin", "gaia-operators": "operator", "internal-audit": "auditor"},
		SecondFactor: SecondFactorRule{Required: true, Claim: "acr", Values: []string{"mfa"}}}
	put := func(o OIDCSettings, secret string) (int, apiError) {
		v := defaultSettings()
		v.OIDC = o
		b, _ := json.Marshal(SettingsUpdate{SigninSettings: v, OIDCClientSecret: secret})
		admin = f.login(store.RoleAdmin)
		return f.call(admin, "PUT", "/api/v1/settings/signin", string(b), f.stepUp(admin, "PUT", "/api/v1/settings/signin"), nil)
	}

	t.Run("§1 OIDC needs a break-glass admin with a security key", func(t *testing.T) {
		f.s.db.SQL().Exec(`UPDATE users SET break_glass = 0`)
		if c, e := put(set, "idp-client-secret"); c != 409 {
			t.Fatalf("without a break-glass admin: %d %+v", c, e)
		}
		f.s.db.SQL().Exec(`UPDATE users SET break_glass = 1 WHERE username = 'admin'`)
		if c, e := put(set, ""); c != 200 {
			t.Fatalf("on: %d %+v", c, e)
		}
		var v SettingsView
		f.call(admin, "GET", "/api/v1/settings/signin", "", nil, &v)
		if !v.OIDCStatus.OK || !v.OIDCSecretSet || !v.OIDCStatus.Device || strings.Contains(fmt.Sprint(v), "idp-client-secret") {
			t.Fatalf("settings view %+v", v.OIDCStatus)
		}
	})

	t.Run("§1 code flow with PKCE, state and nonce; a session for the mapped role", func(t *testing.T) {
		b := newBrowser(t, ts)
		resp, loc := idp.sso(b, "omer")
		if resp.StatusCode != http.StatusFound || loc != "/vms" {
			t.Fatalf("callback: %d -> %s (%s)", resp.StatusCode, loc, lastAudit(t, f).Detail)
		}
		_, body := b.get("/api/v1/me", nil)
		var me Me
		json.Unmarshal(body, &me)
		if me.Role != store.RoleOperator || me.Source != store.SourceOIDC || me.Session.Method != "oidc" || me.Session.IdPState != "ok" {
			t.Fatalf("me %+v", me)
		}
		// A flow cannot be finished from another browser, nor twice.
		b2 := newBrowser(t, ts)
		r1, _ := b.get("/api/v1/oidc/start", nil)
		cb := idp.signIn(r1.Header.Get("Location"), "omer", clk.now())
		u, _ := url.Parse(cb)
		if r, _ := b2.get(u.Path+"?"+u.RawQuery, nil); !strings.Contains(r.Header.Get("Location"), "sso_error=expired") {
			t.Fatalf("another browser: %s", r.Header.Get("Location"))
		}
	})

	t.Run("§1 while OIDC is on only break-glass admins sign in locally; that notifies admins", func(t *testing.T) {
		if c, e, _ := f.password("operator1", userPassword("operator1"), ""); c != 403 || e.Code != "signin_refused" {
			t.Fatalf("a local operator: %d %+v", c, e)
		}
		f.signIn("admin")
		cs := f.stateChanges(t, "signin")
		if len(cs) == 0 || cs[len(cs)-1].To != evBreakglass {
			t.Fatalf("no breakglass_signin: %+v", cs)
		}
		if ev, _ := f.s.classify(ctx, cs[len(cs)-1]); ev == nil || ev.Event != evBreakglass {
			t.Fatalf("not an event: %+v", ev)
		}
		ch, _ := f.s.db.SecurityChanges(ctx, "")
		found := false
		for _, c := range ch {
			found = found || c.To == evBreakglass
		}
		if !found {
			t.Fatal("the break-glass sign-in is not in the daily report's security changes")
		}
	})

	t.Run("§2 the highest role of the user's groups; no matching group, no access", func(t *testing.T) {
		b := newBrowser(t, ts)
		idp.sso(b, "ayla")
		_, body := b.get("/api/v1/me", nil)
		var me Me
		json.Unmarshal(body, &me)
		if me.Role != store.RoleAdmin {
			t.Fatalf("admins+operators: %s", me.Role)
		}
		_, loc := idp.sso(newBrowser(t, ts), "nobody")
		if !strings.Contains(loc, "sso_error=no_role") {
			t.Fatalf("no mapped group: %s", loc)
		}
	})

	t.Run("§3 an ID token without a second factor is refused", func(t *testing.T) {
		_, loc := idp.sso(newBrowser(t, ts), "weak")
		if !strings.Contains(loc, "sso_error=no_second_factor") {
			t.Fatalf("acr 1: %s", loc)
		}
	})

	t.Run("§8 an OIDC-mapped auditor gets an expiry", func(t *testing.T) {
		idp.sso(newBrowser(t, ts), "irem")
		u, _ := f.s.db.UserByName(ctx, "irem")
		if u.Role != store.RoleAuditor || u.ExpiresAt == "" {
			t.Fatalf("%+v", u)
		}
	})

	t.Run("§2 removed from the group at the IdP: access ends within 10 minutes", func(t *testing.T) {
		b := newBrowser(t, ts)
		idp.sso(b, "omer")
		idp.mu.Lock()
		idp.users["omer"].groups = []string{"staff"}
		idp.mu.Unlock()
		clk.add(5 * time.Minute)
		if r, _ := b.get("/api/v1/me", nil); r.StatusCode != 200 {
			t.Fatalf("before the refresh: %d", r.StatusCode)
		}
		clk.add(5 * time.Minute)
		if err := f.s.sweepSessions(ctx); err != nil { // the 30 s sweep, without waiting for a request
			t.Fatal(err)
		}
		r, body := b.get("/api/v1/me", nil)
		if r.StatusCode != 401 || !strings.Contains(string(body), store.EndIdPRemoved) {
			t.Fatalf("10 min after removal: %d %s", r.StatusCode, body)
		}
		idp.users["omer"].groups = []string{"gaia-operators"}
	})

	t.Run("§2 a role change at the IdP ends the user's sessions", func(t *testing.T) {
		b := newBrowser(t, ts)
		idp.sso(b, "omer")
		idp.users["omer"].groups = []string{"internal-audit"}
		clk.add(10 * time.Minute)
		r, body := b.get("/api/v1/me", map[string]string{"X-Gaia-Background": "1"})
		if r.StatusCode != 401 || !strings.Contains(string(body), store.EndRoleChange) {
			t.Fatalf("after a role change: %d %s", r.StatusCode, body)
		}
		idp.users["omer"].groups = []string{"gaia-operators"}
	})

	t.Run("§2 disabled at the IdP: the session ends at the refresh", func(t *testing.T) {
		b := newBrowser(t, ts)
		idp.sso(b, "omer")
		idp.users["omer"].disabled = true
		clk.add(10 * time.Minute)
		if r, _ := b.get("/api/v1/me", nil); r.StatusCode != 401 {
			t.Fatalf("disabled at the IdP: %d", r.StatusCode)
		}
		idp.users["omer"].disabled = false
	})

	t.Run("§4 OIDC step-up: a new sign-in at the IdP with prompt=login, max_age=0, auth_time within 120 s", func(t *testing.T) {
		b := newBrowser(t, ts)
		idp.sso(b, "ayla")
		var ch StepUpChallenge
		if c := b.post("/api/v1/step-up", `{"method":"GET","path":"/api/v1/evidence-export"}`, nil, &ch); c != 200 || ch.Redirect == "" {
			t.Fatalf("step-up: %d %+v", c, ch)
		}
		q, _ := url.Parse(ch.Redirect)
		if q.Query().Get("prompt") != "login" || q.Query().Get("max_age") != "0" {
			t.Fatalf("the step-up redirect: %s", ch.Redirect)
		}
		// An old auth_time does not pass.
		cb := idp.signIn(ch.Redirect, "ayla", clk.now().Add(-5*time.Minute))
		u, _ := url.Parse(cb)
		b.get(u.Path+"?"+u.RawQuery, nil)
		if r, _ := b.get("/api/v1/evidence-export", map[string]string{"X-Gaia-Step-Up": ch.ID}); r.StatusCode != 403 {
			t.Fatalf("with an old auth_time: %d", r.StatusCode)
		}
		b.post("/api/v1/step-up", `{"method":"GET","path":"/api/v1/evidence-export"}`, nil, &ch)
		cb = idp.signIn(ch.Redirect, "ayla", clk.now())
		u, _ = url.Parse(cb)
		b.get(u.Path+"?"+u.RawQuery, nil)
		if r, _ := b.get("/api/v1/evidence-export", map[string]string{"X-Gaia-Step-Up": ch.ID}); r.StatusCode != 200 {
			t.Fatalf("with a fresh sign-in: %d", r.StatusCode)
		}
	})

	t.Run("§2 IdP unreachable: the session goes on, step-up is refused with idp_unreachable", func(t *testing.T) {
		b := newBrowser(t, ts)
		idp.sso(b, "ayla")
		idp.mu.Lock()
		idp.down = true
		idp.mu.Unlock()
		clk.add(10 * time.Minute)
		if r, _ := b.get("/api/v1/me", nil); r.StatusCode != 200 {
			t.Fatalf("with the IdP down: %d", r.StatusCode)
		}
		var e apiError
		if c := b.post("/api/v1/step-up", `{"method":"GET","path":"/api/v1/evidence-export"}`, nil, &e); c != 403 || e.Code != "idp_unreachable" {
			t.Fatalf("step-up with the IdP down: %d %+v", c, e)
		}
		// Break-glass still works.
		f.signIn("admin")
		idp.mu.Lock()
		idp.down = false
		idp.mu.Unlock()
	})

	t.Run("§9 the CLI signs in at the IdP with the device grant (RFC 8628)", func(t *testing.T) {
		var ch DeviceChallenge
		if c, e := f.call("", "POST", "/api/v1/oidc/device", `{"device_id":"cli-device-for-the-device-grant-01"}`, nil, &ch); c != 200 || ch.UserCode == "" {
			t.Fatalf("device start: %d %+v", c, e)
		}
		var st SigninStep
		if f.call("", "POST", "/api/v1/oidc/device/token", fmt.Sprintf(`{"handle":%q}`, ch.Handle), nil, &st); st.Stage != "pending" {
			t.Fatalf("before the user: %+v", st)
		}
		idp.mu.Lock()
		for dc := range idp.devices {
			idp.devices[dc] = "omer"
		}
		idp.mu.Unlock()
		clk.add(6 * time.Second) // the poll interval
		if c, e := f.call("", "POST", "/api/v1/oidc/device/token", fmt.Sprintf(`{"handle":%q}`, ch.Handle), nil, &st); c != 200 || st.Stage != stageDone || st.Token == "" {
			t.Fatalf("after the user: %d %+v %+v", c, e, st)
		}
		var me Me
		f.call(st.Token, "GET", "/api/v1/me", "", nil, &me)
		if me.Session.Kind != "cli" || me.Session.Method != "oidc" || me.Role != store.RoleOperator {
			t.Fatalf("the CLI's session %+v", me)
		}
	})

	t.Run("§1 turning OIDC off ends the OIDC sessions at their next refresh", func(t *testing.T) {
		b := newBrowser(t, ts)
		idp.sso(b, "omer")
		off := set
		off.Enabled = false
		if c, e := put(off, ""); c != 200 {
			t.Fatalf("off: %d %+v", c, e)
		}
		clk.add(10 * time.Minute)
		if err := f.s.sweepSessions(ctx); err != nil {
			t.Fatal(err)
		}
		if r, _ := b.get("/api/v1/me", nil); r.StatusCode != 401 {
			t.Fatalf("an OIDC session after OIDC was turned off: %d", r.StatusCode)
		}
	})
}
