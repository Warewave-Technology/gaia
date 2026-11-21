// Command t26gate is a lab fixture for T26 (lab/lab-vm/t26-gate.sh): it plays browsers, the CLI and the lab IdP's
// users against a gaia server set up for the gate, and checks ADR-008 end to end with a real Keycloak
// (lab/lab-vm/t26-keycloak.sh). Not part of the product.
//
//	f  the T20 role × endpoint matrix, every route from the server's own OpenAPI document, with local users (OIDC off)
//	   and with OIDC users (OIDC on); step-ups are made as a user makes them (a security key; a new sign-in at the IdP)
//	d  an OIDC user removed from its group at the IdP loses access within 10 minutes
//	e  with Keycloak stopped: a break-glass sign-in works and notifies admins (a webhook), and an SSO session goes on
//	   but refuses step-up with idp_unreachable
//	+  the CLI's device grant at the IdP; a local TOTP sign-in; local sign-in refused for others while OIDC is on
//
// It writes OUT/gate.json and OUT/matrix-local.md, OUT/matrix-oidc.md. Exit 0 OK, 2 CRITICAL, 3 UNKNOWN.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/auth"
)

var (
	api       = flag.String("api", "https://gaia-lab.test:8446", "the gate server (its public URL)")
	certFile  = flag.String("cert", "", "the gate server's TLS certificate (PEM)")
	kcURL     = flag.String("kc", "https://gaia-lab.test:8444", "Keycloak")
	kcCA      = flag.String("kc-ca", "/root/t26-keycloak/certs/kc.crt", "Keycloak's certificate")
	kcSecrets = flag.String("kc-secrets", "/root/t26-keycloak/secrets.json", "the lab IdP's secrets (t26-keycloak.sh)")
	kcScript  = flag.String("kc-script", "", "t26-keycloak.sh, to stop and start Keycloak")
	adminPW   = flag.String("admin-pw-file", "", "the gate server's first admin's password")
	out       = flag.String("out", "", "output directory")
	sinkDir   = flag.String("sink-dir", "", "notify-sink's directory (webhooks land in hook/)")
	sinkURL   = flag.String("sink-url", "", "notify-sink's webhook URL")
	sinkCA    = flag.String("sink-ca", "", "notify-sink's certificate (PEM)")
	quick     = flag.Bool("quick", false, "skip d and e (they take about 20 minutes)")
)

type secrets struct {
	ClientSecret string                                    `json:"client_secret"`
	AdminPW      string                                    `json:"admin_password"`
	Users        map[string]struct{ Password, OTP string } `json:"users"`
}

var (
	sec    secrets
	pool   *x509.CertPool
	result = map[string]any{}
	failed []string
)

func check(name string, ok bool, detail any) {
	result[name] = map[string]any{"pass": ok, "detail": detail}
	if !ok {
		failed = append(failed, name)
	}
	log.Printf("%s: %v %v", name, map[bool]string{true: "PASS", false: "FAIL"}[ok], detail)
}

func must(err error, what string) {
	if err != nil {
		log.Printf("UNKNOWN: %s: %v", what, err)
		writeOut()
		os.Exit(3)
	}
}

func writeOut() {
	result["failed"] = failed
	b, _ := json.MarshalIndent(result, "", "  ")
	os.WriteFile(filepath.Join(*out, "gate.json"), append(b, '\n'), 0o644)
}

// --- a browser: cookies, the lab's CAs, no automatic redirects ---

type browser struct {
	cl    *http.Client
	token string // a CLI session's bearer token ("" = the cookie)
}

func newBrowser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{cl: &http.Client{Jar: jar, Timeout: 60 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (b *browser) req(method, u string, body io.Reader, ct string, h map[string]string) (*http.Response, []byte, error) {
	r, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, nil, err
	}
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	if b.token != "" && strings.HasPrefix(u, *api) {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	for k, v := range h {
		r.Header.Set(k, v)
	}
	resp, err := b.cl.Do(r)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp, data, err
}

// gaia calls the gate server's API: the status and the body.
func (b *browser) gaia(method, path, body string, h map[string]string) (int, []byte) {
	var rd io.Reader
	ct := ""
	if body != "" {
		rd, ct = strings.NewReader(body), "application/json"
	}
	resp, data, err := b.req(method, *api+path, rd, ct, h)
	if err != nil {
		return 0, []byte(err.Error())
	}
	return resp.StatusCode, data
}

func code(data []byte) string {
	var e struct{ Code, Reason string }
	json.Unmarshal(data, &e)
	return e.Code
}

// --- a WebAuthn security key in software (as internal/server/softkey_test.go) ---

type softKey struct {
	key   *ecdsa.PrivateKey
	id    []byte
	count uint32
}

func newSoftKey() *softKey {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	id := make([]byte, 32)
	rand.Read(id)
	return &softKey{key: k, id: id}
}

var b64 = base64.RawURLEncoding

func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 256:
		return []byte{major<<5 | 24, byte(n)}
	}
	b := []byte{major<<5 | 25, 0, 0}
	binary.BigEndian.PutUint16(b[1:], uint16(n))
	return b
}
func cborInt(v int) []byte {
	if v >= 0 {
		return cborHead(0, uint64(v))
	}
	return cborHead(1, uint64(-1-v))
}
func cborBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }
func cborText(s string) []byte  { return append(cborHead(3, uint64(len(s))), s...) }

func (k *softKey) authData(attested bool) []byte {
	u, _ := url.Parse(*api)
	h := sha256.Sum256([]byte(u.Hostname()))
	flags := byte(0x05)
	if attested {
		flags |= 0x40
	}
	k.count++
	out := append(h[:], flags, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(out[33:], k.count)
	if attested {
		x, y := make([]byte, 32), make([]byte, 32)
		k.key.PublicKey.X.FillBytes(x)
		k.key.PublicKey.Y.FillBytes(y)
		cose := cborHead(5, 5)
		for _, kv := range [][2][]byte{{cborInt(1), cborInt(2)}, {cborInt(3), cborInt(-7)}, {cborInt(-1), cborInt(1)}, {cborInt(-2), cborBytes(x)}, {cborInt(-3), cborBytes(y)}} {
			cose = append(append(cose, kv[0]...), kv[1]...)
		}
		out = append(out, make([]byte, 16)...)
		out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
		out = append(append(out, k.id...), cose...)
	}
	return out
}

func challengeOf(opts json.RawMessage) string {
	var o struct {
		PublicKey struct{ Challenge string } `json:"publicKey"`
	}
	json.Unmarshal(opts, &o)
	return o.PublicKey.Challenge
}

func (k *softKey) clientData(typ string, opts json.RawMessage) []byte {
	return []byte(fmt.Sprintf(`{"type":%q,"challenge":%q,"origin":%q,"crossOrigin":false}`, typ, challengeOf(opts), *api))
}

func (k *softKey) create(opts json.RawMessage) json.RawMessage {
	cd := k.clientData("webauthn.create", opts)
	att := cborHead(5, 3)
	att = append(append(att, cborText("fmt")...), cborText("none")...)
	att = append(append(att, cborText("attStmt")...), cborHead(5, 0)...)
	att = append(append(att, cborText("authData")...), cborBytes(k.authData(true))...)
	b, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "attestationObject": b64.EncodeToString(att), "transports": []string{"usb"}},
		"clientExtensionResults": map[string]any{}})
	return b
}

func (k *softKey) get(opts json.RawMessage) json.RawMessage {
	cd := k.clientData("webauthn.get", opts)
	ad := k.authData(false)
	h := sha256.Sum256(cd)
	d := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, k.key, d[:])
	b, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad), "signature": b64.EncodeToString(sig)},
		"clientExtensionResults": map[string]any{}})
	return b
}

// --- actors: the users of the gate ---

type actor struct {
	name, role, kind string // kind: local or oidc
	pw               string
	key              *softKey
	totp             []byte
	b                *browser
}

func (a *actor) post(path string, in any, out any) (int, []byte) {
	body, _ := json.Marshal(in)
	st, data := a.b.gaia("POST", path, string(body), nil)
	if out != nil {
		json.Unmarshal(data, out)
	}
	return st, data
}

// signIn signs a local actor in (a CLI session with its key, or TOTP); the first time it enrols the factor.
func (a *actor) signIn() error {
	a.b = newBrowser()
	var st struct {
		Stage       string          `json:"stage"`
		SigninToken string          `json:"signin_token"`
		Token       string          `json:"token"`
		WebAuthn    json.RawMessage `json:"webauthn"`
	}
	if c, data := a.post("/api/v1/login", map[string]any{"username": a.name, "password": a.pw, "device_id": "t26gate-" + a.name}, &st); c != 200 {
		return fmt.Errorf("sign-in of %s: %d %s", a.name, c, data)
	}
	for st.Stage != "done" {
		switch st.Stage {
		case "enrol":
			kind := "webauthn"
			if a.key == nil {
				kind = "totp"
			}
			var ch struct {
				WebAuthn   json.RawMessage `json:"webauthn"`
				TOTPSecret string          `json:"totp_secret"`
			}
			if c, data := a.post("/api/v1/login/enrol", map[string]string{"signin_token": st.SigninToken, "kind": kind}, &ch); c != 200 {
				return fmt.Errorf("enrol %s: %d %s", a.name, c, data)
			}
			fin := map[string]any{"signin_token": st.SigninToken, "kind": kind, "name": "t26gate"}
			if kind == "webauthn" {
				fin["webauthn"] = a.key.create(ch.WebAuthn)
			} else {
				a.totp, _ = auth.ParseTOTPSecret(ch.TOTPSecret)
				fin["code"] = auth.TOTPCode(a.totp, auth.TOTPStepAt(time.Now()))
			}
			if c, data := a.post("/api/v1/login/enrol/finish", fin, &st); c != 200 {
				return fmt.Errorf("enrol finish %s: %d %s", a.name, c, data)
			}
		case "second_factor":
			ans := map[string]any{"signin_token": st.SigninToken}
			if a.key != nil {
				ans["method"], ans["webauthn"] = "webauthn", a.key.get(st.WebAuthn)
			} else {
				ans["method"], ans["code"] = "totp", auth.TOTPCode(a.totp, auth.TOTPStepAt(time.Now()))
			}
			if c, data := a.post("/api/v1/login/factor", ans, &st); c != 200 {
				return fmt.Errorf("factor %s: %d %s", a.name, c, data)
			}
		default:
			return fmt.Errorf("sign-in of %s stopped at %q", a.name, st.Stage)
		}
	}
	a.b.token = st.Token
	return nil
}

var formRe = regexp.MustCompile(`(?s)<form[^>]*action="([^"]*)"[^>]*>(.*?)</form>`)

// kcLogin plays a user at Keycloak from an authorization URL to the redirect back to Gaia (returned).
func (a *actor) kcLogin(start string) (string, error) {
	u := start
	for i := 0; i < 12; i++ {
		resp, data, err := a.b.req("GET", u, nil, "", nil)
		if err != nil {
			return "", err
		}
		for {
			if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusSeeOther {
				loc := resp.Header.Get("Location")
				if strings.HasPrefix(loc, *api) {
					return loc, nil
				}
				if strings.HasPrefix(loc, "/") {
					loc = *kcURL + loc
				}
				u = loc
				break
			}
			if resp.StatusCode != 200 {
				return "", fmt.Errorf("Keycloak answered %d at %s: %.300s", resp.StatusCode, u, data)
			}
			var action string
			form := url.Values{}
			for _, m := range formRe.FindAllSubmatch(data, -1) {
				in := string(m[2])
				switch {
				case strings.Contains(in, `name="password"`): // a re-authentication shows no user name field
					action = string(m[1])
					if strings.Contains(in, `name="username"`) {
						form.Set("username", a.name)
					}
					form.Set("password", a.pw)
					form.Set("credentialId", "")
				case strings.Contains(in, `name="otp"`):
					action = string(m[1])
					form.Set("otp", auth.TOTPCode(a.totp, auth.TOTPStepAt(time.Now())))
					form.Set("login", "Sign In")
				case strings.Contains(in, `name="accept"`):
					action = string(m[1])
					form.Set("accept", "Yes")
				}
				if action != "" {
					break
				}
			}
			if action == "" {
				if bytes.Contains(data, []byte("Device Login Successful")) || bytes.Contains(data, []byte("kc-page-title")) &&
					bytes.Contains(data, []byte("successful")) {
					return "", nil // the device grant's last page
				}
				title := regexp.MustCompile(`(?s)<title>(.*?)</title>`).FindSubmatch(data)
				f := filepath.Join(*out, fmt.Sprintf("kc-page-%d.html", time.Now().UnixNano()))
				os.WriteFile(f, data, 0o600) // to see what the page asks (the lab users' names only; no secrets)
				return "", fmt.Errorf("no form at Keycloak's page %q (%s), saved in %s", title, u, f)
			}
			action = html.UnescapeString(action)
			resp, data, err = a.b.req("POST", action, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
			if err != nil {
				return "", err
			}
		}
	}
	return "", errors.New("too many redirects at Keycloak")
}

// ssoSignIn signs an OIDC actor in through Gaia's code flow: a browser session (the cookie).
func (a *actor) ssoSignIn() error {
	a.b = newBrowser()
	resp, _, err := a.b.req("GET", *api+"/api/v1/oidc/start?next=/", nil, "", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusFound {
		return fmt.Errorf("oidc/start: %d", resp.StatusCode)
	}
	cb, err := a.kcLogin(resp.Header.Get("Location"))
	if err != nil {
		return err
	}
	resp, _, err = a.b.req("GET", cb, nil, "", nil)
	if err != nil {
		return err
	}
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || strings.Contains(loc, "sso_error") {
		return fmt.Errorf("callback for %s: %d -> %s", a.name, resp.StatusCode, loc)
	}
	return nil
}

func (a *actor) again() error {
	if a.kind == "oidc" {
		return a.ssoSignIn()
	}
	return a.signIn()
}

// stepUp makes a step-up as the user would: the key's assertion, or a new sign-in at the IdP.
func (a *actor) stepUp(method, path string) (map[string]string, error) {
	var ch struct {
		ID       string          `json:"id"`
		WebAuthn json.RawMessage `json:"webauthn"`
		Redirect string          `json:"redirect_url"`
	}
	c, data := a.post("/api/v1/step-up", map[string]string{"method": method, "path": path}, &ch)
	if c != 200 {
		return nil, fmt.Errorf("step-up: %d %s", c, data)
	}
	if a.kind == "local" {
		proof, _ := json.Marshal(map[string]any{"method": "webauthn", "webauthn": a.key.get(ch.WebAuthn)})
		return map[string]string{"X-Gaia-Step-Up": ch.ID, "X-Gaia-Step-Up-Proof": b64.EncodeToString(proof)}, nil
	}
	cb, err := a.kcLogin(ch.Redirect)
	if err != nil {
		return nil, err
	}
	resp, _, err := a.b.req("GET", cb, nil, "", nil)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(resp.Header.Get("Location"), "step_up=") {
		return nil, fmt.Errorf("step-up callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	return map[string]string{"X-Gaia-Step-Up": ch.ID}, nil
}

// --- the role × endpoint matrix from the OpenAPI document ---

type op struct {
	method, path, example, stepUp string
	roles                         []string
}

func operations() ([]op, error) {
	st, data := newBrowser().gaia("GET", "/api/v1/openapi.json", "", nil)
	if st != 200 {
		return nil, fmt.Errorf("openapi.json: %d", st)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			Roles   []string `json:"x-gaia-roles"`
			StepUp  string   `json:"x-gaia-step-up"`
			Example string   `json:"x-gaia-example"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var ops []op
	for p, ms := range doc.Paths {
		for m, o := range ms {
			ops = append(ops, op{method: strings.ToUpper(m), path: p, example: o.Example, stepUp: o.StepUp, roles: o.Roles})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].path+ops[i].method < ops[j].path+ops[j].method })
	return ops, nil
}

func matrix(name string, actors map[string]*actor, targetID int64) bool {
	ops, err := operations()
	must(err, "openapi")
	roles := []string{"", "auditor", "operator", "admin"}
	var md strings.Builder
	fmt.Fprintf(&md, "| Endpoint | Allowed | anonymous | auditor | operator | admin |\n|---|---|---|---|---|---|\n")
	bad := []string{}
	anon := &actor{b: newBrowser()}
	for _, o := range ops {
		path := strings.ReplaceAll(o.path, "{sid}", "su_none")
		path = strings.ReplaceAll(path, "{name}", "x")
		if strings.HasPrefix(o.path, "/api/v1/users/{id}") {
			path = strings.ReplaceAll(path, "{id}", fmt.Sprint(targetID))
		} else {
			path = strings.ReplaceAll(path, "{id}", "999999")
		}
		row := fmt.Sprintf("| `%s %s` | %s |", o.method, o.path, strings.Join(o.roles, ", "))
		if len(o.roles) == 0 {
			row = fmt.Sprintf("| `%s %s` | (public) |", o.method, o.path)
		}
		for _, role := range roles {
			a := anon
			if role != "" {
				a = actors[role]
			}
			body := o.example
			// Sign-in steps with wrong answers count as failed sign-ins from this address (ADR-008 §7): the matrix sends
			// the wrong password once, anonymously, and a role's own current password where one is asked for.
			if o.path == "/api/v1/login" && role != "" {
				row += " · |"
				continue
			}
			if o.path == "/api/v1/me/password" && role != "" {
				b, _ := json.Marshal(map[string]string{"current_password": a.pw, "new_password": "short"})
				body = string(b)
			}
			if o.method == "PUT" && o.path == "/api/v1/settings/signin" && role != "" {
				body = currentSettings(actors["admin"]) // the settings in force: the matrix must not change them
			}
			fresh := role != "" && (o.path == "/api/v1/logout" || o.path == "/api/v1/sessions/end-others")
			if fresh {
				x := *a
				must(x.again(), "fresh session for "+o.path)
				a = &x
			}
			st, data := a.b.gaia(o.method, path, body, nil)
			allowed := slices.Contains(o.roles, role)
			if st == 403 && code(data) == "step_up_required" && allowed {
				h, err := a.stepUp(o.method, path)
				if err != nil {
					bad = append(bad, fmt.Sprintf("%s %s as %s: step-up: %v", o.method, o.path, role, err))
				} else {
					st, data = a.b.gaia(o.method, path, body, h)
					row += " ↑"
				}
			}
			row += fmt.Sprintf(" %d |", st)
			switch {
			case len(o.roles) == 0:
				if st == 403 && code(data) == "forbidden" {
					bad = append(bad, fmt.Sprintf("%s %s (public) as %q: 403", o.method, o.path, role))
				}
			case role == "":
				if st != 401 {
					bad = append(bad, fmt.Sprintf("%s %s anonymous: %d", o.method, o.path, st))
				}
			case !allowed:
				if st != 403 {
					bad = append(bad, fmt.Sprintf("%s %s as %s: %d, want 403", o.method, o.path, role, st))
				}
			default:
				if st == 401 || st == 403 {
					bad = append(bad, fmt.Sprintf("%s %s as %s (allowed): %d %s", o.method, o.path, role, st, code(data)))
				}
			}
			if o.path == "/api/v1/sessions/end-others" && st == 200 {
				for _, x := range actors {
					must(x.again(), "sign in again after end-others")
				}
			}
		}
		md.WriteString(row + "\n")
	}
	os.WriteFile(filepath.Join(*out, "matrix-"+name+".md"), []byte(md.String()), 0o644)
	check("f_matrix_"+name, len(bad) == 0, map[string]any{"routes": len(ops), "roles": len(roles), "problems": bad})
	return len(bad) == 0
}

func kcAdminToken(b *browser) (string, error) {
	form := url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {"admin"}, "password": {sec.AdminPW}}
	resp, data, err := b.req("POST", *kcURL+"/realms/master/protocol/openid-connect/token", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", nil)
	if err != nil {
		return "", err
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(data, &t); resp.StatusCode != 200 || t.AccessToken == "" {
		return "", fmt.Errorf("Keycloak admin token: %d", resp.StatusCode)
	}
	return t.AccessToken, nil
}

func kcAdmin(b *browser, tok, method, path string) (int, []byte, error) {
	resp, data, err := b.req(method, *kcURL+"/admin/realms/gaia"+path, nil, "", map[string]string{"Authorization": "Bearer " + tok})
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}

// removeFromGroups removes a Keycloak user from every group (at the IdP, as an IdP admin would).
func removeFromGroups(user string) error {
	b := newBrowser()
	tok, err := kcAdminToken(b)
	if err != nil {
		return err
	}
	_, data, err := kcAdmin(b, tok, "GET", "/users?exact=true&username="+url.QueryEscape(user))
	if err != nil {
		return err
	}
	var us []struct{ ID string }
	if json.Unmarshal(data, &us); len(us) != 1 {
		return fmt.Errorf("Keycloak user %s: %s", user, data)
	}
	_, data, err = kcAdmin(b, tok, "GET", "/users/"+us[0].ID+"/groups")
	if err != nil {
		return err
	}
	var gs []struct{ ID string }
	json.Unmarshal(data, &gs)
	for _, g := range gs {
		if st, _, err := kcAdmin(b, tok, "DELETE", "/users/"+us[0].ID+"/groups/"+g.ID); err != nil || st != 204 {
			return fmt.Errorf("remove from group: %d %v", st, err)
		}
	}
	return nil
}

type me struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Session  struct {
		ID       int64  `json:"id"`
		Method   string `json:"method"`
		IdPState string `json:"idp_state"`
		Kind     string `json:"kind"`
	} `json:"session"`
}

func whoami(a *actor) (int, me) {
	st, data := a.b.gaia("GET", "/api/v1/me", "", map[string]string{"X-Gaia-Background": "1"})
	var m me
	json.Unmarshal(data, &m)
	return st, m
}

func hooks(event string) []string {
	files, _ := filepath.Glob(filepath.Join(*sinkDir, "hook", "*.json"))
	var hit []string
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte(`\"event\":\"`+event+`\"`)) || bytes.Contains(b, []byte(`"event":"`+event+`"`)) {
			hit = append(hit, filepath.Base(f))
		}
	}
	return hit
}

func kc(cmd string) error {
	c := exec.Command(*kcScript, cmd)
	c.Stdout, c.Stderr = io.Discard, os.Stderr
	return c.Run()
}

func main() {
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	b, err := os.ReadFile(*kcSecrets)
	must(err, "Keycloak secrets")
	must(json.Unmarshal(b, &sec), "Keycloak secrets")
	pool = x509.NewCertPool()
	for _, f := range []string{*certFile, *kcCA, *sinkCA} {
		pem, err := os.ReadFile(f)
		must(err, f)
		pool.AppendCertsFromPEM(pem)
	}
	pw, err := os.ReadFile(*adminPW)
	must(err, "admin password")
	t0 := time.Now()

	// Local users: the first admin (break-glass), and others made by it. Every factor is a key in software, but one
	// operator's, which is an authenticator app (TOTP).
	admin := &actor{name: "admin", role: "admin", kind: "local", pw: strings.TrimRight(string(pw), "\r\n"), key: newSoftKey()}
	must(admin.signIn(), "the first admin's first sign-in (enrolment)")
	mk := func(name, role string, key bool) *actor {
		p := "lab gate password " + auth.NewToken("")[:16]
		body, _ := json.Marshal(map[string]string{"username": name, "role": role, "password": p})
		h, err := admin.stepUp("POST", "/api/v1/users")
		must(err, "step-up for POST /api/v1/users")
		if st, data := admin.b.gaia("POST", "/api/v1/users", string(body), h); st != 201 {
			must(fmt.Errorf("%d %s", st, data), "user "+name)
		}
		a := &actor{name: name, role: role, kind: "local", pw: p}
		if key {
			a.key = newSoftKey()
		}
		must(a.signIn(), "first sign-in of "+name)
		return a
	}
	local := map[string]*actor{"admin": admin, "operator": mk("op1", "operator", true), "auditor": mk("aud1", "auditor", true)}
	target := mk("target1", "operator", true)
	var tu struct{ ID int64 }
	_, data := admin.b.gaia("GET", "/api/v1/users", "", nil)
	var us []struct {
		ID       int64
		Username string
	}
	json.Unmarshal(data, &us)
	for _, u := range us {
		if u.Username == target.name {
			tu.ID = u.ID
		}
	}

	// A local operator with an authenticator app: enrolled at the first sign-in, then a sign-in with the next code.
	totp := mk("totp1", "operator", false)
	time.Sleep(time.Duration(30-time.Now().Unix()%30+1) * time.Second)
	err = totp.signIn()
	check("local_totp_signin", err == nil, fmt.Sprint(err))

	// Admins hear of sign-ins through a webhook channel (notify-sink).
	ca, err := os.ReadFile(*sinkCA)
	must(err, "sink CA")
	ch, _ := json.Marshal(map[string]any{"name": "t26-sink", "kind": "webhook", "webhook": map[string]string{"url": *sinkURL, "ca_pem": string(ca)}})
	if st, data := admin.b.gaia("POST", "/api/v1/notification-channels", string(ch), nil); st != 201 {
		must(fmt.Errorf("%d %s", st, data), "webhook channel")
	}

	log.Printf("f: role matrix, local users (OIDC off)")
	matrix("local", local, tu.ID)

	log.Printf("OIDC on")
	set := map[string]any{"idle_timeout_min": 15, "absolute_timeout_h": 8, "password_max_age": "none", "online_breach_check": false,
		"oidc_client_secret": sec.ClientSecret,
		"oidc": map[string]any{"enabled": true, "name": "Lab Keycloak", "issuer": *kcURL + "/realms/gaia", "client_id": "gaia", "groups_claim": "groups",
			"roles":         map[string]string{"gaia-admins": "admin", "gaia-operators": "operator", "internal-audit": "auditor"},
			"second_factor": map[string]any{"required": true, "claim": "acr", "values": []string{"mfa"}}, "ca_pem": string(mustRead(*kcCA))}}
	body, _ := json.Marshal(set)
	h, err := admin.stepUp("PUT", "/api/v1/settings/signin")
	must(err, "step-up for the settings")
	st, data := admin.b.gaia("PUT", "/api/v1/settings/signin", string(body), h)
	if st != 200 {
		must(fmt.Errorf("%d %s", st, data), "OIDC on")
	}
	oidcActor := func(kcName, role string) *actor {
		u := sec.Users[kcName]
		return &actor{name: kcName, role: role, kind: "oidc", pw: u.Password, totp: []byte(u.OTP)}
	}
	oidc := map[string]*actor{"admin": oidcActor("kc-admin", "admin"), "operator": oidcActor("kc-op", "operator"), "auditor": oidcActor("kc-aud", "auditor")}
	for r, a := range oidc {
		must(a.ssoSignIn(), "SSO sign-in of "+a.name)
		st, m := whoami(a)
		if st != 200 || m.Role != r || m.Session.Method != "oidc" {
			must(fmt.Errorf("%d %+v", st, m), "SSO session of "+a.name)
		}
	}
	staff := oidcActor("kc-staff", "")
	err = staff.ssoSignIn()
	check("oidc_no_group_no_access", err != nil && strings.Contains(err.Error(), "sso_error=no_role"), fmt.Sprint(err))
	c, data := (&actor{b: newBrowser()}).post("/api/v1/login", map[string]string{"username": "op1", "password": local["operator"].pw}, nil)
	check("oidc_on_local_operator_refused", c == 403 && code(data) == "signin_refused", fmt.Sprintf("%d %s", c, code(data)))
	err = admin.signIn()
	check("oidc_on_breakglass_admin_signs_in", err == nil, fmt.Sprint(err))

	log.Printf("f: role matrix, OIDC users")
	matrix("oidc", oidc, tu.ID)

	// The CLI at the IdP: the device grant.
	cli := &actor{b: newBrowser()}
	var dc struct {
		Handle, UserCode string
		Complete         string `json:"verification_uri_complete"`
	}
	c, data = cli.post("/api/v1/oidc/device", map[string]string{"device_id": "t26gate-cli"}, &dc)
	var devErr error
	if c != 200 {
		devErr = fmt.Errorf("device start: %d %s", c, data)
	} else {
		u := oidcActor("kc-op", "operator")
		u.b = newBrowser()
		_, devErr = u.kcLogin(dc.Complete)
	}
	var ds struct {
		Stage, Token string
	}
	for i := 0; devErr == nil && i < 20; i++ {
		time.Sleep(6 * time.Second)
		c, data = cli.post("/api/v1/oidc/device/token", map[string]string{"handle": dc.Handle}, &ds)
		if c != 200 {
			devErr = fmt.Errorf("device token: %d %s", c, data)
		}
		if ds.Stage == "done" {
			break
		}
	}
	cli.b.token = ds.Token
	_, dm := whoami(cli)
	check("cli_device_grant", devErr == nil && ds.Stage == "done" && dm.Session.Kind == "cli" && dm.Session.Method == "oidc" && dm.Role == "operator",
		map[string]any{"error": fmt.Sprint(devErr), "stage": ds.Stage, "user": dm.Username, "role": dm.Role, "session_kind": dm.Session.Kind})

	if !*quick {
		// d: removed from the group at the IdP; e: the IdP stopped.
		gone := oidcActor("kc-gone", "operator")
		must(gone.ssoSignIn(), "SSO sign-in of kc-gone")
		signedIn := time.Now()
		must(removeFromGroups("kc-gone"), "remove kc-gone from its groups at Keycloak")
		removed := time.Now()
		log.Printf("d: kc-gone removed from its groups at %s; waiting for its session to end", removed.Format(time.RFC3339))
		var lost time.Time
		var lostBody string
		keep := oidcActor("kc-keep", "admin")
		keepAt := time.Time{}
		for time.Since(removed) < 12*time.Minute {
			if keepAt.IsZero() && time.Since(signedIn) > 5*time.Minute {
				must(keep.ssoSignIn(), "SSO sign-in of kc-keep")
				keepAt = time.Now()
			}
			st, data := gone.b.gaia("GET", "/api/v1/me", "", map[string]string{"X-Gaia-Background": "1"})
			if st == 401 {
				lost, lostBody = time.Now(), string(data)
				break
			}
			time.Sleep(10 * time.Second)
		}
		d := lost.Sub(removed).Seconds()
		check("d_removed_from_group_loses_access", !lost.IsZero() && d <= 600, map[string]any{"signed_in": signedIn.UTC().Format(time.RFC3339),
			"removed": removed.UTC().Format(time.RFC3339), "lost": lost.UTC().Format(time.RFC3339), "seconds_after_removal": d, "answer": lostBody})

		if keepAt.IsZero() {
			must(keep.ssoSignIn(), "SSO sign-in of kc-keep")
			keepAt = time.Now()
		}
		log.Printf("e: Keycloak stopped")
		must(kc("stop"), "stop Keycloak")
		stopped := time.Now()
		bg := &actor{name: "admin", role: "admin", kind: "local", pw: admin.pw, key: admin.key}
		bgErr := bg.signIn()
		var hits []string
		for i := 0; i < 12 && len(hits) == 0; i++ {
			time.Sleep(5 * time.Second)
			hits = hooks("breakglass_signin")
		}
		check("e_breakglass_signin_with_idp_down", bgErr == nil && len(hits) > 0, map[string]any{"error": fmt.Sprint(bgErr), "webhooks": hits})
		// kc-keep's session refreshes at most 10 minutes after its sign-in: then the IdP does not answer.
		var km me
		for time.Since(keepAt) < 11*time.Minute {
			_, km = whoami(keep)
			if km.Session.IdPState == "idp_unreachable" {
				break
			}
			time.Sleep(15 * time.Second)
		}
		st, kmd := keep.b.gaia("GET", "/api/v1/views/fleet", "", nil)
		sc, sd := keep.post("/api/v1/step-up", map[string]string{"method": "GET", "path": "/api/v1/evidence-export"}, nil)
		check("e_sso_session_continues_step_up_refused", km.Session.IdPState == "idp_unreachable" && st == 200 && sc == 403 && code(sd) == "idp_unreachable",
			map[string]any{"idp_state": km.Session.IdPState, "request": st, "step_up": sc, "step_up_code": code(sd), "fleet": len(kmd) > 0,
				"stopped": stopped.UTC().Format(time.RFC3339), "keep_signed_in": keepAt.UTC().Format(time.RFC3339)})
		must(kc("start"), "start Keycloak")
	}
	result["seconds"] = time.Since(t0).Seconds()
	writeOut()
	if len(failed) > 0 {
		log.Printf("CRITICAL: %v", failed)
		os.Exit(2)
	}
	log.Printf("OK")
}

// currentSettings is the settings in force, as a PUT takes them.
func currentSettings(a *actor) string {
	_, cur := a.b.gaia("GET", "/api/v1/settings/signin", "", nil)
	var v map[string]json.RawMessage
	json.Unmarshal(cur, &v)
	keep := map[string]json.RawMessage{}
	for _, k := range []string{"oidc", "idle_timeout_min", "absolute_timeout_h", "password_max_age", "online_breach_check", "breach_check_url"} {
		keep[k] = v[k]
	}
	b, _ := json.Marshal(keep)
	return string(b)
}

func mustRead(p string) []byte {
	b, err := os.ReadFile(p)
	must(err, p)
	return b
}
