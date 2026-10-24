package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// SecretReader reads bao:// references, for OIDC client secrets.
type SecretReader interface {
	Read(ctx context.Context, ref string) (map[string]string, error)
}

// WithSecrets lets OIDC providers use client secrets from OpenBao.
func (a *Service) WithSecrets(r SecretReader) *Service {
	a.secrets = r
	return a
}

// pendingCookie carries an OIDC sign-in that still needs Gaia's passkey.
const pendingCookie = "gaia_pending"

// discoveryTTL is how long an issuer's discovery document is kept.
const discoveryTTL = time.Hour

type discovered struct {
	p  *oidc.Provider
	at time.Time
}

type oidcCache struct {
	mu sync.Mutex
	m  map[string]discovered
}

func (a *Service) discover(ctx context.Context, issuer string) (*oidc.Provider, error) {
	a.oidc.mu.Lock()
	defer a.oidc.mu.Unlock()
	if d, ok := a.oidc.m[issuer]; ok && a.now().Sub(d.at) < discoveryTTL {
		return d.p, nil
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, a.httpClient()), issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc %s: %w", issuer, err)
	}
	if a.oidc.m == nil {
		a.oidc.m = map[string]discovered{}
	}
	a.oidc.m[issuer] = discovered{p: p, at: a.now()}
	return p, nil
}

func (a *Service) httpClient() *http.Client {
	if a.client != nil {
		return a.client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// oidcConfig returns a provider's settings, its discovery and the OAuth2
// client for it.
func (a *Service) oidcConfig(ctx context.Context, name string) (*catalog.AuthProvider, *oidc.Provider, *oauth2.Config, error) {
	cfg, _, err := a.config()
	if err != nil {
		return nil, nil, nil, err
	}
	var prov *catalog.AuthProvider
	for i := range cfg.Providers {
		if cfg.Providers[i].Name == name && cfg.Providers[i].Type == "oidc" {
			prov = &cfg.Providers[i]
		}
	}
	if prov == nil {
		return nil, nil, nil, fmt.Errorf("no OIDC provider %q", name)
	}
	p, err := a.discover(ctx, prov.Issuer)
	if err != nil {
		return nil, nil, nil, err
	}
	secret := ""
	if prov.ClientSecret != "" {
		if a.secrets == nil {
			return nil, nil, nil, errors.New("this server cannot read OpenBao for the client secret")
		}
		f, err := a.secrets.Read(ctx, prov.ClientSecret)
		if err != nil {
			return nil, nil, nil, err
		}
		secret = f["client_secret"]
	}
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	for _, s := range prov.Scopes {
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	oc := &oauth2.Config{
		ClientID: prov.ClientID, ClientSecret: secret, Endpoint: p.Endpoint(), Scopes: scopes,
		RedirectURL: strings.TrimRight(cfg.URL, "/") + "/auth/oidc/" + url.PathEscape(name) + "/callback",
	}
	return prov, p, oc, nil
}

type oidcFlow struct {
	Provider string `json:"provider"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
}

// oidcStart sends the browser to the identity provider, with state, nonce
// and PKCE.
func (a *Service) oidcStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("provider")
	_, _, oc, err := a.oidcConfig(ctx, name)
	if err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	f := oidcFlow{Provider: name, Nonce: random(), Verifier: oauth2.GenerateVerifier(), Next: safeNext(r.URL.Query().Get("next"))}
	state, err := a.saveFlow(ctx, "oidc", "", f)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, oc.AuthCodeURL(state, oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)), http.StatusFound)
}

// claims are the ID token's claims Gaia uses.
type claims struct {
	Subject  string   `json:"sub"`
	Username string   `json:"preferred_username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Verified bool     `json:"email_verified"`
	AMR      []string `json:"amr"`
	ACR      string   `json:"acr"`
	Groups   []string
}

// oidcCallback checks the provider's answer, finds or creates the person
// and signs them in, or asks for Gaia's passkey when the provider did not
// show MFA.
func (a *Service) oidcCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		a.fail(w, fmt.Errorf("the identity provider refused: %s", e), http.StatusUnauthorized)
		return
	}
	var f oidcFlow
	if _, err := a.takeFlow(ctx, q.Get("state"), "oidc", &f); err != nil || f.Provider != r.PathValue("provider") {
		a.fail(w, errors.New("this sign-in expired or was not started here; start again"), http.StatusBadRequest)
		return
	}
	prov, p, oc, err := a.oidcConfig(ctx, f.Provider)
	if err != nil {
		a.fail(w, err, http.StatusBadGateway)
		return
	}
	cctx := oidc.ClientContext(ctx, a.httpClient())
	tok, err := oc.Exchange(cctx, q.Get("code"), oauth2.VerifierOption(f.Verifier))
	if err != nil {
		a.fail(w, fmt.Errorf("the code exchange failed: %w", err), http.StatusUnauthorized)
		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		a.fail(w, errors.New("no ID token"), http.StatusUnauthorized)
		return
	}
	idt, err := p.VerifierContext(cctx, &oidc.Config{ClientID: prov.ClientID, Now: a.now}).Verify(cctx, raw)
	if err != nil {
		a.fail(w, fmt.Errorf("the ID token is not valid: %w", err), http.StatusUnauthorized)
		return
	}
	if idt.Nonce != f.Nonce {
		a.fail(w, errors.New("the ID token's nonce does not match"), http.StatusUnauthorized)
		return
	}
	var c claims
	if err := idt.Claims(&c); err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	if prov.GroupsClaim != "" {
		var all map[string]any
		_ = idt.Claims(&all)
		c.Groups = stringList(all[prov.GroupsClaim])
	}
	userID, err := a.oidcUser(ctx, prov.Name, c)
	if err != nil {
		a.fail(w, err, http.StatusForbidden)
		return
	}
	if mfaShown(prov, c) {
		if err := a.uiSession(ctx, w, r, userID, true); err != nil {
			a.fail(w, err, http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, f.Next, http.StatusFound)
		return
	}
	// No MFA evidence: Gaia's own passkey finishes the sign-in.
	pending, err := a.saveFlow(ctx, "oidc-pending", userID, map[string]string{"next": f.Next})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	cfg, _, _ := a.config()
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: pending, Path: "/", MaxAge: int(flowTTL / time.Second),
		HttpOnly: true, Secure: strings.HasPrefix(cfg.URL, "https://"), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login?step=passkey&next="+url.QueryEscape(f.Next), http.StatusFound)
}

// mfaShown reports whether the ID token shows MFA the provider is trusted
// for (design 11.2).
func mfaShown(prov *catalog.AuthProvider, c claims) bool {
	if prov.MFA == nil || !prov.MFA.TrustIdp {
		return false
	}
	for _, m := range c.AMR {
		if slices.Contains(prov.MFA.AcceptAmr, m) {
			return true
		}
	}
	return c.ACR != "" && slices.Contains(prov.MFA.AcceptAcr, c.ACR)
}

// oidcUser finds the person by provider and subject, creating them on
// their first sign-in, and replaces their teams from the groups claim.
func (a *Service) oidcUser(ctx context.Context, provider string, c claims) (string, error) {
	if c.Subject == "" {
		return "", errors.New("the ID token has no subject")
	}
	q := a.store.Q()
	u, err := q.UserBySubject(ctx, db.UserBySubjectParams{Provider: provider, Subject: text(c.Subject)})
	switch {
	case err == nil:
		if !u.Enabled {
			return "", errors.New("this account is disabled")
		}
	case errors.Is(err, pgx.ErrNoRows):
		name := strings.ToLower(c.Username)
		if !validUsername(name) {
			name = strings.ToLower(strings.SplitN(c.Email, "@", 2)[0])
		}
		if !validUsername(name) {
			name = "user-" + strings.ToLower(random()[:8])
		}
		if _, err := q.UserByName(ctx, name); err == nil {
			// The name belongs to someone else (a local account): keep both.
			name = name + "." + provider
		}
		u = db.User{ID: store.NewUserID(), Username: name}
		if err := q.InsertUser(ctx, db.InsertUserParams{ID: u.ID, Username: name, DisplayName: text(c.Name), Provider: provider, Subject: text(c.Subject)}); err != nil {
			return "", err
		}
	default:
		return "", err
	}
	return u.ID, a.store.Tx(ctx, func(q *db.Queries) error {
		// The provider's verified address is where personal notifications
		// go; it follows the provider at every sign-in.
		if c.Email != "" && c.Verified {
			if err := q.SetEmail(ctx, db.SetEmailParams{ID: u.ID, Email: text(c.Email)}); err != nil {
				return err
			}
		}
		if err := q.ClearTeams(ctx, db.ClearTeamsParams{UserID: u.ID, Source: "oidc"}); err != nil {
			return err
		}
		for _, g := range c.Groups {
			if err := q.AddTeam(ctx, db.AddTeamParams{UserID: u.ID, Team: strings.TrimPrefix(g, "/"), Source: "oidc"}); err != nil {
				return err
			}
		}
		return nil
	})
}

// pending returns the OIDC sign-in waiting for a passkey, without using
// it up.
func (a *Service) pending(r *http.Request) (string, string, error) {
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		return "", "", errors.New("no sign-in is waiting for a passkey")
	}
	f, err := a.store.Q().PeekFlow(r.Context(), c.Value)
	if err != nil || f.Kind != "oidc-pending" {
		return "", "", errors.New("the sign-in expired; start again")
	}
	var data map[string]string
	_ = json.Unmarshal(f.Data, &data)
	return f.UserID.String, data["next"], nil
}

// pendingBegin starts the passkey step of an OIDC sign-in: an assertion
// with one of the person's passkeys, or registering their first one.
func (a *Service) pendingBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, _, err := a.pending(r)
	if err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	u, err := a.webauthnUser(ctx, userID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if len(u.creds) == 0 {
		f, err := a.beginRegister(ctx, userID)
		if err != nil {
			a.fail(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"flow": f.Flow, "options": f.Options, "register": true})
		return
	}
	rp, err := a.relyingParty()
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	opts, sd, err := rp.BeginLogin(u)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	id, err := a.saveFlow(ctx, "webauthn-step", userID, sd)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"flow": id, "options": opts, "register": false})
}

// pendingFinish checks the passkey and signs the person in.
func (a *Service) pendingFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, next, err := a.pending(r)
	if err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	var b finishBody
	if !decode(w, r, &b) {
		return
	}
	var codes []string
	f, err := a.store.Q().PeekFlow(ctx, b.Flow)
	if err != nil {
		a.fail(w, errors.New("this sign-in step expired; start again"), http.StatusUnauthorized)
		return
	}
	if f.Kind == "webauthn-register" {
		// The person's first passkey.
		got, err := a.finishRegister(ctx, b.Flow, b.Name, b.Credential)
		if err != nil || got != userID {
			a.fail(w, errPasskey, http.StatusUnauthorized)
			return
		}
		if codes, err = a.newRecoveryCodes(ctx, userID); err != nil {
			a.fail(w, err, http.StatusInternalServerError)
			return
		}
	} else {
		var sd webauthn.SessionData
		if _, err := a.takeFlow(ctx, b.Flow, "webauthn-step", &sd); err != nil {
			a.fail(w, err, http.StatusUnauthorized)
			return
		}
		if _, err := a.assert(ctx, sd, userID, b.Credential); err != nil {
			a.fail(w, err, http.StatusUnauthorized)
			return
		}
	}
	if c, err := r.Cookie(pendingCookie); err == nil {
		_, _ = a.store.Q().TakeFlow(ctx, db.TakeFlowParams{ID: c.Value, Kind: "oidc-pending"})
	}
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	if err := a.uiSession(ctx, w, r, userID, true); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	out := map[string]any{"next": next}
	if codes != nil {
		out["recoveryCodes"] = codes
	}
	writeJSON(w, http.StatusOK, out)
}

// safeNext keeps redirects on this site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func stringList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = append(out, x)
	}
	return out
}
