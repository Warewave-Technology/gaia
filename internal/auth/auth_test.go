package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/pquerna/otp/totp"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// rp is Gaia as the lab example's AuthConfig describes it.
var rp = virtualwebauthn.RelyingParty{ID: "localhost", Name: "Gaia", Origin: "http://localhost:8080"}

type env struct {
	t     *testing.T
	store *store.Store
	auth  *Service
	srv   *httptest.Server
}

func labGraph(t *testing.T) *catalog.Graph { return labGraphWith(t, nil) }

// labGraphWith is the lab example with files changed by edit.
func labGraphWith(t *testing.T, edit func(path, content string) string) *catalog.Graph {
	t.Helper()
	root := filepath.Join("..", "..", "examples", "lab")
	var files []catalog.File
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if edit != nil {
			data = []byte(edit(rel, string(data)))
		}
		files = append(files, catalog.File{Path: rel, Data: data})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Load("c0ffee", files)
	if err != nil {
		t.Fatal(err)
	}
	g, err := catalog.Resolve(cat)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func setup(t *testing.T) *env { return setupWith(t, labGraph(t)) }

func setupWith(t *testing.T, g *catalog.Graph) *env {
	t.Helper()
	itest.Require(t)
	ctx := context.Background()
	dsn := itest.StartPostgres(t)
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	a := New(s, func() *catalog.Graph { return g }, nil).WithTransit(memTransit{})
	mux := http.NewServeMux()
	a.Mount(mux)
	mux.HandleFunc("GET /test/any", a.Guard("", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, From(r.Context())) }))
	mux.HandleFunc("POST /test/admin", a.Guard("platform-admin", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, store: s, auth: a, srv: srv}
}

// browser is a cookie-keeping client with one virtual authenticator.
type browser struct {
	t    *testing.T
	base string
	c    *http.Client
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func (e *env) browser(userID string) *browser {
	jar, _ := cookiejar.New(nil)
	a := virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{UserHandle: []byte(userID)})
	return &browser{t: e.t, base: e.srv.URL, c: &http.Client{Jar: jar}, auth: a, cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)}
}

func (b *browser) do(method, path string, body any, headers ...string) (int, map[string]any) {
	b.t.Helper()
	var r io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		r = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, b.base+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(CSRFHeader, "1")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			req.Header.Del(headers[i])
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &out)
	return resp.StatusCode, out
}

func options(t *testing.T, start map[string]any) string {
	t.Helper()
	b, err := json.Marshal(start["options"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// enroll registers the browser's passkey with an enrollment link and
// returns the recovery codes.
func (b *browser) enroll(link string) []string {
	b.t.Helper()
	token := link[strings.Index(link, "#")+1:]
	code, start := b.do("POST", "/api/v1/auth/enroll/begin", map[string]string{"token": token})
	if code != 200 {
		b.t.Fatalf("enroll begin: %d %v", code, start)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(options(b.t, start))
	if err != nil {
		b.t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAttestationResponse(rp, b.auth, b.cred, *opts)
	b.auth.AddCredential(b.cred)
	code, done := b.do("POST", "/api/v1/auth/enroll/finish", map[string]any{"token": token, "flow": start["flow"], "name": "laptop", "credential": json.RawMessage(resp)})
	if code != 200 {
		b.t.Fatalf("enroll finish: %d %v", code, done)
	}
	var codes []string
	for _, c := range done["recoveryCodes"].([]any) {
		codes = append(codes, c.(string))
	}
	return codes
}

func (b *browser) login() (int, map[string]any) {
	b.t.Helper()
	code, start := b.do("POST", "/api/v1/auth/passkey/begin", nil)
	if code != 200 {
		b.t.Fatalf("login begin: %d %v", code, start)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(options(b.t, start))
	if err != nil {
		b.t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAssertionResponse(rp, b.auth, b.cred, *opts)
	return b.do("POST", "/api/v1/auth/passkey/finish", map[string]any{"flow": start["flow"], "credential": json.RawMessage(resp)})
}

func (e *env) user(name string, teams ...string) (string, string) {
	e.t.Helper()
	link, err := e.auth.CreateLocalUser(context.Background(), name, "", teams)
	if err != nil {
		e.t.Fatal(err)
	}
	u, err := e.store.Q().UserByName(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID, link
}

func TestEnrollLoginAndLogout(t *testing.T) {
	e := setup(t)
	id, link := e.user("ayse", "team-platform")
	if !strings.HasPrefix(link, "http://localhost:8080/enroll#") {
		t.Fatalf("link = %s", link)
	}
	b := e.browser(id)
	codes := b.enroll(link)
	if len(codes) != recoveryCodes || len(codes[0]) != 11 {
		t.Errorf("recovery codes = %v", codes)
	}
	// Enrolling signs in.
	code, me := b.do("GET", "/api/v1/me", nil)
	if code != 200 || me["username"] != "ayse" || !strings.Contains(strings.Join(anyStrings(me["roles"]), ","), "platform-admin") {
		t.Fatalf("me: %d %v", code, me)
	}
	// The link works once.
	code, _ = e.browser(id).do("POST", "/api/v1/auth/enroll/begin", map[string]string{"token": link[strings.Index(link, "#")+1:]})
	if code != http.StatusUnauthorized {
		t.Errorf("second use of the link: %d", code)
	}

	if code, _ := b.do("POST", "/api/v1/auth/logout", nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := b.do("GET", "/api/v1/me", nil); code != 401 {
		t.Errorf("after logout: %d", code)
	}

	// A new browser with the same authenticator signs in by passkey alone.
	b2 := e.browser(id)
	b2.auth, b2.cred = b.auth, b.cred
	if code, p := b2.login(); code != 200 || p["username"] != "ayse" {
		t.Fatalf("login: %d %v", code, p)
	}
	if code, _ := b2.do("POST", "/test/admin", nil); code != 204 {
		t.Errorf("admin endpoint: %d", code)
	}
	// The counter moved, so a replayed or cloned assertion is caught.
	creds, _ := e.store.Q().Credentials(context.Background(), id)
	if len(creds) != 1 || !creds[0].LastUsedAt.Valid {
		t.Errorf("credentials = %+v", creds)
	}
}

func TestAPasskeyForAnotherSiteIsRefused(t *testing.T) {
	e := setup(t)
	id, link := e.user("selin")
	b := e.browser(id)
	b.enroll(link)
	code, start := b.do("POST", "/api/v1/auth/passkey/begin", nil)
	if code != 200 {
		t.Fatal(code)
	}
	opts, _ := virtualwebauthn.ParseAssertionOptions(options(t, start))
	evil := virtualwebauthn.RelyingParty{ID: "localhost", Name: "Gaia", Origin: "http://evil.example"}
	resp := virtualwebauthn.CreateAssertionResponse(evil, b.auth, b.cred, *opts)
	if code, _ := e.browser(id).do("POST", "/api/v1/auth/passkey/finish", map[string]any{"flow": start["flow"], "credential": json.RawMessage(resp)}); code != 401 {
		t.Errorf("an assertion for another origin: %d", code)
	}
	// The flow is used up even by a failed attempt.
	resp = virtualwebauthn.CreateAssertionResponse(rp, b.auth, b.cred, *opts)
	if code, _ := e.browser(id).do("POST", "/api/v1/auth/passkey/finish", map[string]any{"flow": start["flow"], "credential": json.RawMessage(resp)}); code != 401 {
		t.Errorf("a flow used twice: %d", code)
	}
}

func TestCSRFAndRoles(t *testing.T) {
	e := setup(t)
	id, link := e.user("mehmet", "team-web")
	b := e.browser(id)
	b.enroll(link)
	if code, _ := b.do("POST", "/test/admin", nil); code != 403 {
		t.Errorf("viewer on an admin endpoint: %d", code)
	}
	// Without the header a cookie-authenticated change is refused.
	if code, _ := b.do("POST", "/api/v1/auth/logout", nil, CSRFHeader, ""); code != 403 {
		t.Errorf("logout without the CSRF header: %d", code)
	}
	if code, _ := e.browser(id).do("GET", "/test/any", nil); code != 401 {
		t.Errorf("no session: %d", code)
	}
	// Sign-in endpoints take JSON with the header only.
	if code, _ := b.do("POST", "/api/v1/auth/passkey/begin", nil, "Content-Type", "application/x-www-form-urlencoded"); code != 403 {
		t.Errorf("form post to sign-in: %d", code)
	}
}

func TestRecoveryCodeIsUsedOnce(t *testing.T) {
	e := setup(t)
	id, link := e.user("can")
	codes := e.browser(id).enroll(link)
	b := e.browser(id)
	if code, out := b.do("POST", "/api/v1/auth/recovery", map[string]string{"username": "can", "code": strings.ToUpper(codes[0])}); code != 200 {
		t.Fatalf("recovery: %d %v", code, out)
	}
	if code, _ := b.do("GET", "/api/v1/me", nil); code != 200 {
		t.Errorf("after recovery: %d", code)
	}
	if code, _ := e.browser(id).do("POST", "/api/v1/auth/recovery", map[string]string{"username": "can", "code": codes[0]}); code != 401 {
		t.Errorf("the same code twice: %d", code)
	}
	if code, _ := e.browser(id).do("POST", "/api/v1/auth/recovery", map[string]string{"username": "nobody", "code": codes[1]}); code != 401 {
		t.Errorf("wrong user: %d", code)
	}
}

func TestDisabledAccountsAndExpiredSessions(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id, link := e.user("ops", "team-platform")
	b := e.browser(id)
	b.enroll(link)
	other, link2 := e.user("leaver")
	lb := e.browser(other)
	lb.enroll(link2)

	if code, _ := b.do("POST", "/api/v1/users/"+other+"/disable", nil); code != 204 {
		t.Fatalf("disable: %d", code)
	}
	if code, _ := lb.do("GET", "/api/v1/me", nil); code != 401 {
		t.Errorf("a disabled account kept its session: %d", code)
	}
	if code, _ := lb.login(); code != 401 {
		t.Errorf("a disabled account signed in: %d", code)
	}
	if code, _ := b.do("POST", "/api/v1/users/"+id+"/disable", nil); code != 409 {
		t.Errorf("disabling yourself: %d", code)
	}

	// An idle browser session ends after AuthConfig's idle time (30m).
	e.auth.now = func() time.Time { return time.Now().Add(31 * time.Minute) }
	if code, _ := b.do("GET", "/api/v1/me", nil); code != 401 {
		t.Errorf("idle session still valid: %d", code)
	}
	e.auth.now = time.Now

	// Sessions are listed and can be ended one by one.
	_, _ = b.login()
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/me/sessions", nil)
	resp, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []SessionInfo
	_ = json.NewDecoder(resp.Body).Decode(&sessions)
	_ = resp.Body.Close()
	if len(sessions) < 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	n, _ := e.store.Q().UserSessions(ctx, id)
	if len(n) != len(sessions) {
		t.Errorf("%d sessions listed, %d live", len(sessions), len(n))
	}
}

func TestPasskeysNeedARecentPasskey(t *testing.T) {
	e := setup(t)
	id, link := e.user("ela")
	b := e.browser(id)
	b.enroll(link)
	if code, _ := b.do("POST", "/api/v1/me/passkeys/begin", nil); code != 200 {
		t.Errorf("add a passkey right after enrolling: %d", code)
	}
	e.auth.now = func() time.Time { return time.Now().Add(15 * time.Minute) }
	defer func() { e.auth.now = time.Now }()
	// Keep the session itself alive past its idle time for this check.
	if _, err := e.store.Pool().Exec(context.Background(), "update sessions set last_seen_at = now() + interval '15 minutes'"); err != nil {
		t.Fatal(err)
	}
	code, out := b.do("POST", "/api/v1/me/passkeys/begin", nil)
	if code != 403 || out["stepUp"] != true {
		t.Errorf("add a passkey 15 minutes later: %d %v", code, out)
	}
	if code, _ := b.do("DELETE", "/api/v1/me/passkeys/x", nil); code != 403 {
		t.Errorf("delete without step-up: %d", code)
	}
}

func TestAccess(t *testing.T) {
	g := labGraph(t)
	a := New(nil, func() *catalog.Graph { return g }, nil)
	owner := &Principal{Teams: []string{"team-platform"}, Roles: []string{"viewer"}}
	other := &Principal{Teams: []string{"team-web"}, Roles: []string{"viewer"}}
	ops := &Principal{Teams: []string{"team-ops"}, Roles: []string{"viewer", "operator"}}
	if a.Access(owner, "poc-web") != Full || a.Access(other, "poc-web") != Name || a.Access(ops, "poc-web") != Full {
		t.Error("access to poc-web")
	}
	if a.Access(other, "gone-contract") != Name || a.Access(ops, "gone-contract") != Full {
		t.Error("access to a contract that is not in the catalog")
	}
	if got := strings.Join(g.RolesOf([]string{"team-ops"}), ","); got != "viewer,operator" {
		t.Errorf("roles = %s", got)
	}
	if got := strings.Join(g.RolesOf([]string{"team-platform"}), ","); got != "viewer,platform-admin,operator" {
		t.Errorf("platform-admin's roles = %s", got)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// memTransit "encrypts" by prefixing, enough to see that the store never
// holds the seed itself.
type memTransit struct{}

func (memTransit) Encrypt(_ context.Context, key string, p []byte) (string, error) {
	return "mem:" + key + ":" + base64.StdEncoding.EncodeToString(p), nil
}

func (memTransit) Decrypt(_ context.Context, key, c string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(c, "mem:"+key+":"))
}

func mfaGraph(t *testing.T) *catalog.Graph {
	return labGraphWith(t, func(path, c string) string {
		if path == "platform/auth.yaml" {
			return strings.Replace(c, "require: passkey", "require: mfa", 1)
		}
		return c
	})
}

func TestPasswordAndTOTP(t *testing.T) {
	e := setupWith(t, mfaGraph(t))
	ctx := context.Background()
	id, link := e.user("deniz")
	token := link[strings.Index(link, "#")+1:]
	b := e.browser(id)

	code, setup := b.do("POST", "/api/v1/auth/enroll/totp/begin", map[string]string{"token": token})
	if code != 200 || !strings.HasPrefix(setup["uri"].(string), "otpauth://totp/Gaia:deniz") {
		t.Fatalf("totp begin: %d %v", code, setup)
	}
	secret := setup["secret"].(string)
	row, _ := e.store.Q().TOTP(ctx, id)
	if strings.Contains(row.Ciphertext, secret) {
		t.Error("the seed is stored in clear")
	}
	now, _ := totp.GenerateCode(secret, time.Now())
	if code, _ := b.do("POST", "/api/v1/auth/enroll/totp/finish", map[string]string{"token": token, "password": "short", "code": now}); code != 400 {
		t.Errorf("short password: %d", code)
	}
	if code, out := b.do("POST", "/api/v1/auth/enroll/totp/finish", map[string]string{"token": token, "password": "a long enough password", "code": now}); code != 200 || len(out["recoveryCodes"].([]any)) != recoveryCodes {
		t.Fatalf("totp finish: %d %v", code, out)
	}
	u, _ := e.store.Q().UserByName(ctx, "deniz")
	if !strings.HasPrefix(u.PasswordHash.String, "$argon2id$") {
		t.Errorf("password hash = %q", u.PasswordHash.String)
	}

	login := func(password, code string) int {
		c, _ := e.browser(id).do("POST", "/api/v1/auth/password", map[string]string{"username": "deniz", "password": password, "code": code})
		return c
	}
	// The code that confirmed the seed is used up.
	if c := login("a long enough password", now); c != 401 {
		t.Errorf("reused code: %d", c)
	}
	later := time.Now().Add(30 * time.Second)
	e.auth.now = func() time.Time { return later }
	next, _ := totp.GenerateCode(secret, later)
	if c := login("wrong password!!", next); c != 401 {
		t.Errorf("wrong password: %d", c)
	}
	if c := login("a long enough password", next); c != 200 {
		t.Errorf("password and code: %d", c)
	}
	if c := login("a long enough password", next); c != 401 {
		t.Errorf("the same code twice: %d", c)
	}
	if c, _ := e.browser(id).do("POST", "/api/v1/auth/password", map[string]string{"username": "nobody", "password": "x", "code": next}); c != 401 {
		t.Errorf("unknown user: %d", c)
	}
}

func TestPasskeyOnlyRefusesPasswords(t *testing.T) {
	e := setup(t)
	id, link := e.user("kaan")
	token := link[strings.Index(link, "#")+1:]
	if code, _ := e.browser(id).do("POST", "/api/v1/auth/enroll/totp/begin", map[string]string{"token": token}); code != 400 {
		t.Errorf("totp under require: passkey: %d", code)
	}
	if code, _ := e.browser(id).do("POST", "/api/v1/auth/password", map[string]string{"username": "kaan", "password": "x", "code": "1"}); code != 401 {
		t.Errorf("password under require: passkey: %d", code)
	}
}

func TestPasswordHash(t *testing.T) {
	h := hashPassword("correct horse battery")
	if !checkPassword(h, "correct horse battery") || checkPassword(h, "correct horse batterY") || checkPassword("garbage", "x") {
		t.Error("password check")
	}
	if a, b := hashPassword("same"), hashPassword("same"); a == b {
		t.Error("no salt")
	}
}
