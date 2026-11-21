package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Single sign-on with OIDC (ADR-008 §1-§4, §9): the authorization code flow with PKCE, state and nonce, for a
// confidential client; issuer discovery; the IdP's keys cached and fetched again for an unknown kid (go-oidc). Roles
// come from a groups claim through an explicit mapping, read again at every session refresh (every 10 minutes). The
// CLI signs in with the device authorization grant (RFC 8628) when the IdP offers it; the server talks to the IdP, so
// the client secret never leaves it.

const (
	oidcRefreshEvery = 10 * time.Minute
	oidcFlowFor      = 10 * time.Minute
	idpUnreachable   = "idp_unreachable"
	oidcCookie       = "__Host-gaia_oidc" // binds a flow to the browser that started it (SameSite=Lax: the IdP's redirect is cross-site)
)

// OIDCStatus is what Gaia last saw of the IdP.
type OIDCStatus struct {
	Enabled   bool   `json:"enabled"`
	CheckedAt string `json:"checked_at"` // the last discovery
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Device    bool   `json:"device_grant"` // the IdP offers the device authorization grant (the CLI's sign-in)
}

type oidcClient struct {
	mu       sync.Mutex
	srv      *Server
	set      OIDCSettings
	ready    bool
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	http     *http.Client
	device   string // the device authorization endpoint ("" = none)
	st       OIDCStatus
	flows    map[string]*oidcFlow   // by state
	devices  map[string]*deviceFlow // by the CLI's handle
}

type oidcFlow struct {
	nonce, verifier, binding, next, stepUp string
	created                                time.Time
}

type deviceFlow struct {
	deviceCode, verifier, deviceID, addr, userAgent string
	created, expires                                time.Time
	interval                                        time.Duration
	lastPoll                                        time.Time
}

func newOIDCClient(s *Server) *oidcClient {
	return &oidcClient{srv: s, flows: map[string]*oidcFlow{}, devices: map[string]*deviceFlow{}}
}

// reset drops what was discovered: the settings changed.
func (o *oidcClient) reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ready = false
}

func (o *oidcClient) status() OIDCStatus {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.st
}

func httpClientFor(caPEM string) (*http.Client, error) {
	if caPEM == "" {
		return &http.Client{Timeout: 15 * time.Second}, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("ca_pem holds no certificate")
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}, nil
}

// discover reads the IdP's discovery document for settings set.
func (o *oidcClient) discover(ctx context.Context, set OIDCSettings) error {
	hc, err := httpClientFor(set.CAPEM)
	if err != nil {
		return err
	}
	ctx = oidc.ClientContext(ctx, hc)
	p, err := oidc.NewProvider(ctx, set.Issuer)
	if err != nil {
		return err
	}
	var extra struct {
		Device string `json:"device_authorization_endpoint"`
	}
	if err := p.Claims(&extra); err != nil {
		return err
	}
	secret, err := o.srv.oidcSecret()
	if err != nil {
		return err
	}
	o.set, o.provider, o.http, o.device, o.ready = set, p, hc, extra.Device, true
	o.verifier = p.Verifier(&oidc.Config{ClientID: set.ClientID, Now: o.srv.now})
	o.oauth = oauth2.Config{ClientID: set.ClientID, ClientSecret: secret, Endpoint: p.Endpoint(),
		RedirectURL: o.srv.cfg.PublicURL + "/api/v1/oidc/callback", Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
	return nil
}

// check discovers the IdP of settings that are not saved yet.
func (o *oidcClient) check(ctx context.Context, set OIDCSettings) error {
	tmp := newOIDCClient(o.srv)
	err := tmp.discover(ctx, set)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.st = OIDCStatus{Enabled: set.Enabled, CheckedAt: o.srv.now().UTC().Format(time.RFC3339), OK: err == nil, Device: tmp.device != ""}
	if err != nil {
		o.st.Error = err.Error()
	}
	return err
}

// get returns the client ready for the settings in force; errOIDCOff when OIDC is off.
var errOIDCOff = errors.New("single sign-on is off")

func (o *oidcClient) get(ctx context.Context) (*oidcClient, error) {
	st, err := o.srv.settings(ctx)
	if err != nil {
		return nil, err
	}
	if !st.OIDC.Enabled {
		return nil, fmt.Errorf("%w: %w", store.ErrInvalid, errOIDCOff)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ready {
		return o, nil
	}
	err = o.discover(ctx, st.OIDC)
	o.st = OIDCStatus{Enabled: true, CheckedAt: o.srv.now().UTC().Format(time.RFC3339), OK: err == nil, Device: o.device != ""}
	if err != nil {
		o.st.Error = err.Error()
		return nil, fmt.Errorf("%w: the identity provider: %v", errHost, err)
	}
	return o, nil
}

func (o *oidcClient) ctx(ctx context.Context) context.Context {
	return context.WithValue(oidc.ClientContext(ctx, o.http), oauth2.HTTPClient, o.http)
}

// oidcClaims are what Gaia reads from an ID token.
type oidcClaims struct {
	Subject  string `json:"sub"`
	Name     string `json:"preferred_username"`
	AuthTime int64  `json:"auth_time"`
	Nonce    string `json:"nonce"`
	ACR      string `json:"acr"`
	AMR      []string
	Groups   []string
	raw      map[string]any
}

// verifyID checks an ID token and reads its claims (groups from the configured claim).
func (o *oidcClient) verifyID(ctx context.Context, raw string) (oidcClaims, error) {
	var c oidcClaims
	tok, err := o.verifier.Verify(o.ctx(ctx), raw)
	if err != nil {
		return c, err
	}
	if err := tok.Claims(&c); err != nil {
		return c, err
	}
	if err := tok.Claims(&c.raw); err != nil {
		return c, err
	}
	strs := func(v any) []string {
		var out []string
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
		case string:
			out = strings.Fields(x)
		}
		return out
	}
	c.AMR = strs(c.raw["amr"])
	c.Groups = strs(c.raw[o.set.GroupsClaim])
	c.Subject = tok.Issuer + " " + tok.Subject
	return c, nil
}

// secondFactor reports whether claims show a second factor as the settings require (§3).
func (o *oidcClient) secondFactor(c oidcClaims) bool {
	r := o.set.SecondFactor
	if !r.Required {
		return true
	}
	if r.Claim == "acr" {
		return slices.Contains(r.Values, c.ACR)
	}
	for _, v := range c.AMR {
		if slices.Contains(r.Values, v) {
			return true
		}
	}
	return false
}

// role maps groups to the highest role they give (§2); "" means no access.
func (o *oidcClient) role(groups []string) string {
	best := ""
	for _, g := range groups {
		g = strings.TrimPrefix(g, "/") // Keycloak's full group path
		if r, ok := o.set.Roles[g]; ok && store.RoleRank(r) > store.RoleRank(best) {
			best = r
		}
	}
	return best
}

func (o *oidcClient) putFlow(f *oidcFlow) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k, v := range o.flows {
		if o.srv.now().Sub(v.created) > oidcFlowFor {
			delete(o.flows, k)
		}
	}
	state := auth.NewToken("st_")
	o.flows[state] = f
	return state
}

func (o *oidcClient) takeFlow(state string) *oidcFlow {
	o.mu.Lock()
	defer o.mu.Unlock()
	f := o.flows[state]
	delete(o.flows, state)
	if f != nil && o.srv.now().Sub(f.created) > oidcFlowFor {
		return nil
	}
	return f
}

// authURL starts a code flow; extra are prompt and max_age for a step-up.
func (o *oidcClient) authURL(f *oidcFlow, extra ...oauth2.AuthCodeOption) string {
	f.verifier = oauth2.GenerateVerifier()
	f.nonce = auth.NewToken("n_")
	f.created = o.srv.now()
	state := o.putFlow(f)
	opts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(f.verifier), oidc.Nonce(f.nonce)}, extra...)
	if o.set.SecondFactor.Required && o.set.SecondFactor.Claim == "acr" {
		opts = append(opts, oauth2.SetAuthURLParam("acr_values", strings.Join(o.set.SecondFactor.Values, " ")))
	}
	return o.oauth.AuthCodeURL(state, opts...)
}

// oidcStart sends a browser to the IdP.
func (s *Server) oidcStart(c *call) (int, any, error) {
	o, err := s.oidc.get(c.r.Context())
	if err != nil {
		return 0, nil, err
	}
	f := &oidcFlow{binding: auth.NewToken("b_"), next: safeNext(c.r.URL.Query().Get("next"))}
	u := o.authURL(f)
	http.SetCookie(c.w, &http.Cookie{Name: oidcCookie, Value: f.binding, Path: "/", MaxAge: int(oidcFlowFor / time.Second), HttpOnly: true,
		Secure: true, SameSite: http.SameSiteLaxMode})
	c.detail = "sent to the identity provider"
	return redirect(c, u)
}

// oidcStepUpURL is where an OIDC user signs in again for a step-up.
func (s *Server) oidcStepUpURL(ctx context.Context, id string) (string, error) {
	o, err := s.oidc.get(ctx)
	if err != nil {
		return "", err
	}
	f := &oidcFlow{stepUp: id, next: "/"}
	return o.authURL(f, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0")), nil
}

func redirect(c *call, to string) (int, any, error) {
	c.raw = true
	c.w.Header().Set("Cache-Control", "no-store")
	http.Redirect(c.w, c.r, to, http.StatusFound)
	return http.StatusFound, nil, nil
}

// safeNext is a path on this site only, never an absolute URL from the query.
func safeNext(v string) string {
	if v != "" && strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") && !strings.HasPrefix(v, "/\\") {
		return v
	}
	return "/"
}

// oidcCallback is where the IdP sends the browser back. A sign-in ends in a session; a step-up in a passed step-up.
// A refusal goes to the sign-in page with its reason (sso_error).
func (s *Server) oidcCallback(c *call) (int, any, error) {
	ctx := c.r.Context()
	q := c.r.URL.Query()
	fail := func(code, why string) (int, any, error) {
		c.detail = "single sign-on refused: " + why
		return redirect(c, "/login?sso_error="+url.QueryEscape(code))
	}
	f := s.oidc.takeFlow(q.Get("state"))
	if f == nil {
		return fail("expired", "unknown or expired state")
	}
	if e := q.Get("error"); e != "" {
		return fail("idp_error", "the identity provider answered "+e+": "+q.Get("error_description"))
	}
	if f.stepUp == "" {
		ck, err := c.r.Cookie(oidcCookie)
		if err != nil || ck.Value != f.binding {
			return fail("expired", "the flow was started in another browser")
		}
		http.SetCookie(c.w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	}
	o, err := s.oidc.get(ctx)
	if err != nil {
		return fail("idp_unreachable", err.Error())
	}
	tok, err := o.oauth.Exchange(o.ctx(ctx), q.Get("code"), oauth2.VerifierOption(f.verifier))
	if err != nil {
		return fail("idp_error", "code exchange: "+err.Error())
	}
	rawID, _ := tok.Extra("id_token").(string)
	cl, err := o.verifyID(ctx, rawID)
	if err != nil {
		return fail("bad_token", "ID token: "+err.Error())
	}
	if cl.Nonce != f.nonce {
		return fail("bad_token", "the ID token's nonce is not this flow's")
	}
	c.target = "oidc:" + cl.Name
	if !o.secondFactor(cl) {
		return fail("no_second_factor", fmt.Sprintf("the ID token shows no second factor (%s must be one of %v)", o.set.SecondFactor.Claim, o.set.SecondFactor.Values))
	}
	if f.stepUp != "" {
		return s.oidcStepUpPassed(c, f, cl)
	}
	role := o.role(cl.Groups)
	if role == "" {
		return fail("no_role", fmt.Sprintf("%s is in no group that maps to a role (groups %v)", cl.Name, cl.Groups))
	}
	u, err := s.db.UpsertOIDCUser(ctx, cl.Subject, cl.Name, role)
	if err != nil {
		return 0, nil, err
	}
	if u.Disabled || u.Expired(s.now()) {
		return fail("account_disabled", u.Username+" is disabled or expired in Gaia")
	}
	st, err := s.settings(ctx)
	if err != nil {
		return 0, nil, err
	}
	ps := &pendingSignin{userID: u.ID, username: u.Username, kind: "browser", deviceID: s.deviceCookie(c), userAgent: c.r.UserAgent(),
		addr: remote(c.r), method: "oidc"}
	c.detail = "signed in at the identity provider as " + cl.Name + ", role " + role
	if _, _, err := s.startSession(c, ps, u, st, tok.RefreshToken); err != nil {
		return 0, nil, err
	}
	return redirect(c, f.next)
}

func (s *Server) oidcStepUpPassed(c *call, f *oidcFlow, cl oidcClaims) (int, any, error) {
	su := s.stepups.get(f.stepUp)
	back := "/?step_up=" + url.QueryEscape(f.stepUp)
	now := s.now()
	switch {
	case su == nil:
		c.detail = "step-up at the identity provider: the step-up expired"
	case now.Sub(time.Unix(cl.AuthTime, 0)) > stepUpWindow || cl.AuthTime == 0:
		c.detail = "step-up at the identity provider failed: auth_time is not within 120 seconds"
	default:
		u, err := s.db.User(c.r.Context(), su.userID)
		if err != nil {
			return 0, nil, err
		}
		if u.OIDCSubject != cl.Subject {
			c.detail = "step-up at the identity provider failed: signed in as another user (" + cl.Name + ")"
			break
		}
		su.passedAt, su.passedBy = now, "signed in again at the identity provider"
		c.detail = "step-up passed at the identity provider for " + su.method + " " + su.path
	}
	return redirect(c, back)
}

// refreshOIDC reads an OIDC session's user again at the IdP (§2): every 10 minutes. The user removed, disabled or
// without a mapped group ends the session; a role change ends all the user's sessions; an IdP that does not answer
// marks the session idp_unreachable, which goes on until its limit without step-up.
func (s *Server) refreshOIDC(ctx context.Context, sess store.Session) error {
	o, err := s.oidc.get(ctx)
	if errors.Is(err, errOIDCOff) {
		return s.db.EndSession(ctx, sess.ID, store.EndIdPRemoved, "system") // single sign-on was turned off
	}
	if err != nil {
		return s.db.MarkIdP(ctx, sess.ID, idpUnreachable, "")
	}
	ts := o.oauth.TokenSource(o.ctx(ctx), &oauth2.Token{RefreshToken: sess.IdPRefreshToken(), Expiry: time.Unix(1, 0)})
	tok, err := ts.Token()
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || (re.Response != nil && (re.Response.StatusCode == http.StatusBadRequest || re.Response.StatusCode == http.StatusUnauthorized))) {
		s.log.Info("an OIDC session ends: the identity provider refused its refresh", "session", sess.ID, "user", sess.Username, "error", re.ErrorCode)
		return s.db.EndSession(ctx, sess.ID, store.EndIdPRemoved, "system")
	}
	if err != nil {
		s.log.Warn("the identity provider did not answer a session refresh", "session", sess.ID, "error", err)
		return s.db.MarkIdP(ctx, sess.ID, idpUnreachable, "")
	}
	rawID, _ := tok.Extra("id_token").(string)
	cl, err := o.verifyID(ctx, rawID)
	if err != nil {
		s.log.Warn("an OIDC session ends: the refreshed ID token does not verify", "session", sess.ID, "error", err)
		return s.db.EndSession(ctx, sess.ID, store.EndIdPRemoved, "system")
	}
	role := o.role(cl.Groups)
	if role == "" {
		return s.db.EndSession(ctx, sess.ID, store.EndIdPRemoved, "system")
	}
	if role != sess.Role {
		_, err := s.db.UpsertOIDCUser(ctx, cl.Subject, cl.Name, role) // ends the user's sessions (role_change)
		return err
	}
	return s.db.MarkIdP(ctx, sess.ID, "ok", tok.RefreshToken)
}

// sessionSweep ends sessions past their limits and refreshes the OIDC sessions that are due, every 30 seconds, so a
// session ends when it should even if no request comes.
const sweepEvery = 30 * time.Second

func (s *Server) sessionSweep(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		if err := s.sweepSessions(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("session sweep", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) sweepSessions(ctx context.Context) error {
	st, err := s.settings(ctx)
	if err != nil {
		return err
	}
	if err := s.db.EndStaleSessions(ctx, store.SessionLimits{Idle: st.idle()}); err != nil {
		return err
	}
	// Due before the next sweep counts as due now: a refresh is never later than 10 minutes after the last one.
	due, err := s.db.OIDCSessionsDue(ctx, oidcRefreshEvery-sweepEvery)
	if err != nil {
		return err
	}
	for _, ss := range due {
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := s.refreshOIDC(rctx, ss)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// DeviceStart starts the CLI's sign-in at the IdP.
type DeviceStart struct {
	DeviceID string `json:"device_id,omitempty"`
}

// DeviceChallenge is what the user does in a browser.
type DeviceChallenge struct {
	Handle                  string `json:"handle"` // the CLI polls with it
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresAt               string `json:"expires_at"`
	IntervalS               int    `json:"interval_s"`
}

// DevicePoll asks whether the user has signed in.
type DevicePoll struct {
	Handle string `json:"handle"`
}

func (o *oidcClient) form(ctx context.Context, endpoint string, v url.Values, out any) (int, error) {
	v.Set("client_id", o.oauth.ClientID)
	v.Set("client_secret", o.oauth.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(v.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return resp.StatusCode, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp.StatusCode, nil
}

func (s *Server) oidcDeviceStart(c *call) (int, any, error) {
	var in DeviceStart
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	o, err := s.oidc.get(ctx)
	if err != nil {
		return 0, nil, err
	}
	if o.device == "" {
		return 0, nil, fmt.Errorf("%w: the identity provider offers no device authorization grant: use gaia login -browser", errNotImplemented)
	}
	var r struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URI        string `json:"verification_uri"`
		URIC       string `json:"verification_uri_complete"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
		Error      string `json:"error"`
		ErrorDesc  string `json:"error_description"`
	}
	// PKCE for the device grant too (an IdP may require it of every grant of the client, as Keycloak does).
	verifier := oauth2.GenerateVerifier()
	if _, err := o.form(o.ctx(ctx), o.device, url.Values{"scope": {"openid profile email"}, "code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)},
		"code_challenge_method": {"S256"}}, &r); err != nil || r.DeviceCode == "" {
		return 0, nil, fmt.Errorf("%w: device authorization: %v %s %s", errHost, err, r.Error, r.ErrorDesc)
	}
	now := s.now()
	d := &deviceFlow{deviceCode: r.DeviceCode, verifier: verifier, deviceID: in.DeviceID, addr: remote(c.r), userAgent: c.r.UserAgent(), created: now,
		expires: now.Add(time.Duration(max(r.ExpiresIn, 60)) * time.Second), interval: time.Duration(max(r.Interval, 5)) * time.Second}
	h := auth.NewToken("dv_")
	o.mu.Lock()
	for k, v := range o.devices {
		if now.After(v.expires) {
			delete(o.devices, k)
		}
	}
	o.devices[auth.SHA256Hex(h)] = d
	o.mu.Unlock()
	c.detail = "device sign-in started at the identity provider"
	return 200, DeviceChallenge{Handle: h, UserCode: r.UserCode, VerificationURI: r.URI, VerificationURIComplete: r.URIC,
		ExpiresAt: d.expires.UTC().Format(time.RFC3339), IntervalS: int(d.interval / time.Second)}, nil
}

func (s *Server) oidcDevicePoll(c *call) (int, any, error) {
	var in DevicePoll
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	o, err := s.oidc.get(ctx)
	if err != nil {
		return 0, nil, err
	}
	key := auth.SHA256Hex(in.Handle)
	now := s.now()
	o.mu.Lock()
	d := o.devices[key]
	tooSoon := d != nil && now.Sub(d.lastPoll) < d.interval
	if d != nil && !tooSoon {
		d.lastPoll = now
	}
	o.mu.Unlock()
	if d == nil || now.After(d.expires) {
		return 0, nil, fmt.Errorf("%w: the device sign-in expired or is unknown: start again", errUnauthorized)
	}
	if tooSoon {
		return 200, SigninStep{Stage: "pending"}, nil
	}
	var r struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
	}
	if _, err := o.form(o.ctx(ctx), o.oauth.Endpoint.TokenURL, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {d.deviceCode}, "code_verifier": {d.verifier}}, &r); err != nil {
		return 0, nil, fmt.Errorf("%w: %v", errHost, err)
	}
	switch r.Error {
	case "":
	case "authorization_pending", "slow_down":
		return 200, SigninStep{Stage: "pending"}, nil
	default:
		o.mu.Lock()
		delete(o.devices, key)
		o.mu.Unlock()
		c.detail = "device sign-in refused by the identity provider: " + r.Error
		return 0, nil, fmt.Errorf("%w: the identity provider answered %s", errUnauthorized, r.Error)
	}
	o.mu.Lock()
	delete(o.devices, key)
	o.mu.Unlock()
	cl, err := o.verifyID(ctx, r.IDToken)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: ID token: %v", errUnauthorized, err)
	}
	c.target = "oidc:" + cl.Name
	if !o.secondFactor(cl) {
		c.detail = "device sign-in refused: no second factor in the ID token"
		return 0, nil, fmt.Errorf("%w: the ID token shows no second factor", errSignin)
	}
	role := o.role(cl.Groups)
	if role == "" {
		c.detail = "device sign-in refused: no group maps to a role"
		return 0, nil, fmt.Errorf("%w: %s is in no group that maps to a role", errSignin, cl.Name)
	}
	u, err := s.db.UpsertOIDCUser(ctx, cl.Subject, cl.Name, role)
	if err != nil {
		return 0, nil, err
	}
	if u.Disabled || u.Expired(now) {
		return 0, nil, fmt.Errorf("%w: %s is disabled or expired in Gaia", errSignin, u.Username)
	}
	st, err := s.settings(ctx)
	if err != nil {
		return 0, nil, err
	}
	c.detail = "signed in at the identity provider (device grant) as " + cl.Name + ", role " + role
	ps := &pendingSignin{userID: u.ID, username: u.Username, kind: "cli", deviceID: d.deviceID, userAgent: d.userAgent, addr: d.addr, method: "oidc"}
	return s.startSession(c, ps, u, st, r.RefreshToken)
}
