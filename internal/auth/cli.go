package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/audit"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// Tokens are what the CLI keeps after signing in (design 11.3): a refresh
// token for the session, and a short access token minted from it.
type Tokens struct {
	AccessToken      string    `json:"accessToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshToken     string    `json:"refreshToken,omitempty"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt,omitzero"`
}

const (
	cliCodeTTL   = 2 * time.Minute
	deviceTTL    = 10 * time.Minute
	deviceWait   = 5 * time.Second
	userCodeSize = 8
)

// issueCLI starts a CLI session: a refresh token and its first access
// token.
func (a *Service) issueCLI(ctx context.Context, r *http.Request, userID string) (*Tokens, error) {
	cfg, _, err := a.config()
	if err != nil {
		return nil, err
	}
	_, _, accessTTL, refreshTTL := cfg.Sessions.Durations()
	refresh, refreshID, refreshExp, err := a.newSession(ctx, session{userID: userID, kind: KindCLIRefresh, ttl: refreshTTL, r: r})
	if err != nil {
		return nil, err
	}
	access, _, accessExp, err := a.newSession(ctx, session{userID: userID, kind: KindCLIAccess, parent: refreshID, ttl: min(accessTTL, refreshTTL), r: r})
	if err != nil {
		return nil, err
	}
	return &Tokens{AccessToken: access, AccessExpiresAt: accessExp, RefreshToken: refresh, RefreshExpiresAt: refreshExp}, nil
}

// Refresh mints a new access token from a live refresh token.
func (a *Service) Refresh(ctx context.Context, r *http.Request, refresh string) (*Tokens, error) {
	cfg, _, err := a.config()
	if err != nil {
		return nil, err
	}
	s, err := a.store.Q().SessionByToken(ctx, hashToken(refresh))
	if err != nil || s.Kind != KindCLIRefresh || s.RevokedAt.Valid || !s.Enabled || !a.now().Before(s.ExpiresAt.Time) {
		return nil, ErrUnauthenticated
	}
	_, _, accessTTL, _ := cfg.Sessions.Durations()
	ttl := min(accessTTL, s.ExpiresAt.Time.Sub(a.now()))
	access, _, exp, err := a.newSession(ctx, session{userID: s.UserID, kind: KindCLIAccess, parent: s.ID, ttl: ttl, r: r})
	if err != nil {
		return nil, err
	}
	if err := a.store.Q().TouchSession(ctx, s.ID); err != nil {
		return nil, err
	}
	return &Tokens{AccessToken: access, AccessExpiresAt: exp}, nil
}

// loopbackRedirect accepts only http://127.0.0.1, [::1] or localhost
// with a port: the CLI listens there for the code (RFC 8252).
func loopbackRedirect(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Port() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("redirect_uri must be http://127.0.0.1 with a port")
	}
	h := u.Hostname()
	if h != "localhost" {
		if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
			return nil, errors.New("redirect_uri must point to this machine")
		}
	}
	return u, nil
}

type cliCode struct {
	Challenge string `json:"challenge"`
	Redirect  string `json:"redirect"`
}

// cliAuthorize is the browser's "yes" to a CLI sign-in on this machine: it
// returns where to send the one-time code.
func (a *Service) cliAuthorize(w http.ResponseWriter, r *http.Request) {
	var b struct{ RedirectURI, State, CodeChallenge string }
	if !decode(w, r, &b) {
		return
	}
	p := From(r.Context())
	if p.SessionKind != KindUI {
		problem(w, http.StatusForbidden, "approve CLI sign-ins from the browser")
		return
	}
	u, err := loopbackRedirect(b.RedirectURI)
	if err != nil || len(b.CodeChallenge) < 43 || b.State == "" {
		problem(w, http.StatusBadRequest, "bad CLI sign-in request")
		return
	}
	code, err := a.saveFlowTTL(r.Context(), "cli-code", p.UserID, cliCode{Challenge: b.CodeChallenge, Redirect: u.String()}, cliCodeTTL)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	q := u.Query()
	q.Set("code", code)
	q.Set("state", b.State)
	u.RawQuery = q.Encode()
	writeJSON(w, http.StatusOK, map[string]string{"redirect": u.String()})
}

// cliToken trades the code and the PKCE verifier for tokens.
func (a *Service) cliToken(w http.ResponseWriter, r *http.Request) {
	var b struct{ Code, CodeVerifier, RedirectURI string }
	if !decode(w, r, &b) {
		return
	}
	var c cliCode
	userID, err := a.takeFlow(r.Context(), b.Code, "cli-code", &c)
	sum := sha256.Sum256([]byte(b.CodeVerifier))
	if err != nil || base64.RawURLEncoding.EncodeToString(sum[:]) != c.Challenge || !sameRedirect(c.Redirect, b.RedirectURI) {
		problem(w, http.StatusUnauthorized, "the code is not valid")
		return
	}
	t, err := a.issueCLI(r.Context(), r, userID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// sameRedirect compares the redirect a code was made for with the one the
// CLI names, without the code and state Gaia added.
func sameRedirect(made, named string) bool {
	m, err1 := url.Parse(made)
	n, err2 := url.Parse(named)
	if err1 != nil || err2 != nil {
		return false
	}
	return m.Scheme == n.Scheme && m.Host == n.Host && m.Path == n.Path
}

func (a *Service) cliRefresh(w http.ResponseWriter, r *http.Request) {
	var b struct{ RefreshToken string }
	if !decode(w, r, &b) {
		return
	}
	t, err := a.Refresh(r.Context(), r, b.RefreshToken)
	if err != nil {
		problem(w, http.StatusUnauthorized, "sign in again")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// userCode is what a person types into the browser: no look-alikes.
func userCode() string {
	const alphabet = "BCDFGHJKLMNPQRSTVWXZ"
	b := make([]byte, userCodeSize)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func normalizeUserCode(c string) string {
	c = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(c), "-", ""))
	if len(c) != userCodeSize {
		return ""
	}
	return c[:4] + "-" + c[4:]
}

// deviceStart begins a device sign-in: the CLI shows the user code and
// polls; a person approves it in a browser where they are signed in.
func (a *Service) deviceStart(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := a.config()
	if err != nil {
		a.fail(w, err, http.StatusServiceUnavailable)
		return
	}
	device, hash := newToken()
	user := userCode()
	err = a.store.Q().InsertDeviceCode(r.Context(), db.InsertDeviceCodeParams{
		CodeHash: hash, UserCode: user, Ip: text(clientIP(r)), UserAgent: text(trunc(r.UserAgent(), 300)),
		ExpiresAt: pgtype.Timestamptz{Time: a.now().Add(deviceTTL), Valid: true},
	})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceCode": device, "userCode": user, "verificationUri": strings.TrimRight(cfg.URL, "/") + "/device",
		"interval": int(deviceWait / time.Second), "expiresIn": int(deviceTTL / time.Second),
	})
}

// deviceInfo shows a person where the request comes from before they
// approve it (design 11.3: device sign-in can be phished).
func (a *Service) deviceInfo(w http.ResponseWriter, r *http.Request) {
	d, err := a.store.Q().DeviceCodeByUserCode(r.Context(), normalizeUserCode(r.PathValue("code")))
	if err != nil || d.Status != "pending" {
		problem(w, http.StatusNotFound, "no such code, or it expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ip": d.Ip.String, "userAgent": d.UserAgent.String, "requestedAt": d.CreatedAt.Time})
}

func (a *Service) deviceDecide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := From(r.Context())
		if p.SessionKind != KindUI {
			problem(w, http.StatusForbidden, "approve device sign-ins from the browser")
			return
		}
		status := "denied"
		if approve {
			status = "approved"
		}
		n, err := a.store.Q().DecideDeviceCode(r.Context(), db.DecideDeviceCodeParams{
			UserCode: normalizeUserCode(r.PathValue("code")), Status: status, UserID: text(p.UserID),
		})
		if err != nil {
			a.fail(w, err, http.StatusInternalServerError)
			return
		}
		if n == 0 {
			problem(w, http.StatusNotFound, "no such code, or it expired")
			return
		}
		action := "auth.device.deny"
		if approve {
			action = "auth.device.approve"
		}
		a.record(r, action, "", nil)
		w.WriteHeader(http.StatusNoContent)
	}
}

// deviceToken is the CLI's poll. Its errors follow RFC 8628.
func (a *Service) deviceToken(w http.ResponseWriter, r *http.Request) {
	var b struct{ DeviceCode string }
	if !decode(w, r, &b) {
		return
	}
	ctx := r.Context()
	hash := hashToken(b.DeviceCode)
	d, err := a.store.Q().DeviceCode(ctx, hash)
	switch {
	case err != nil:
		problem(w, http.StatusBadRequest, "invalid_grant")
		return
	case !a.now().Before(d.ExpiresAt.Time):
		problem(w, http.StatusBadRequest, "expired_token")
		return
	case d.Status == "denied":
		problem(w, http.StatusBadRequest, "access_denied")
		return
	case d.Status == "pending":
		code := "authorization_pending"
		if d.PolledAt.Valid && a.now().Sub(d.PolledAt.Time) < deviceWait {
			code = "slow_down"
		}
		_ = a.store.Q().PollDeviceCode(ctx, hash)
		problem(w, http.StatusBadRequest, code)
		return
	}
	n, err := a.store.Q().UseDeviceCode(ctx, hash)
	if err != nil || n != 1 {
		problem(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	t, err := a.issueCLI(ctx, r, d.UserID.String)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// stepUpBegin asks the person's passkey to confirm the current browser
// session for sensitive work (approvals, adding keys).
func (a *Service) stepUpBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := From(ctx)
	if p.SessionKind != KindUI {
		problem(w, http.StatusForbidden, "confirm in the browser")
		return
	}
	// Someone without a passkey confirms with TOTP, even where passkeys
	// cannot work (an AuthConfig url WebAuthn refuses).
	u, err := a.webauthnUser(ctx, p.UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if len(u.creds) == 0 {
		problem(w, http.StatusConflict, "register a passkey first")
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
	id, err := a.saveFlow(ctx, "webauthn-stepup", p.UserID, sd)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, flowStart{Flow: id, Options: opts})
}

func (a *Service) stepUpFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := From(ctx)
	var b finishBody
	if !decode(w, r, &b) {
		return
	}
	var sd webauthn.SessionData
	userID, err := a.takeFlow(ctx, b.Flow, "webauthn-stepup", &sd)
	if err != nil || userID != p.UserID {
		problem(w, http.StatusUnauthorized, "this confirmation expired; start again")
		return
	}
	if _, err := a.assert(ctx, sd, p.UserID, b.Credential); err != nil {
		a.audit(ctx, r, audit.Entry{Actor: p.Username, ActorID: p.UserID, SessionID: p.SessionID, Action: "auth.step-up", Result: "denied", Detail: map[string]string{"method": "passkey"}})
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	if err := a.store.Q().SetStrongAuth(ctx, p.SessionID); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	a.record(r, "auth.step-up", "", map[string]string{"method": "passkey"})
	w.WriteHeader(http.StatusNoContent)
}

// stepUpTOTP confirms the current browser session with a TOTP code, for
// local accounts that sign in with a password and MFA.
func (a *Service) stepUpTOTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := From(ctx)
	if p.SessionKind != KindUI {
		problem(w, http.StatusForbidden, "confirm in the browser")
		return
	}
	var b struct{ Code string }
	if !decode(w, r, &b) {
		return
	}
	if err := a.checkTOTP(ctx, p.UserID, b.Code, false); err != nil {
		a.audit(ctx, r, audit.Entry{Actor: p.Username, ActorID: p.UserID, SessionID: p.SessionID, Action: "auth.step-up", Result: "denied", Detail: map[string]string{"method": "totp"}})
		problem(w, http.StatusUnauthorized, "the code is not valid")
		return
	}
	if err := a.store.Q().SetStrongAuth(ctx, p.SessionID); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	a.record(r, "auth.step-up", "", map[string]string{"method": "totp"})
	w.WriteHeader(http.StatusNoContent)
}

// cliLogout ends the CLI session a refresh token belongs to, with its
// access tokens.
func (a *Service) cliLogout(w http.ResponseWriter, r *http.Request) {
	var b struct{ RefreshToken string }
	if !decode(w, r, &b) {
		return
	}
	s, err := a.store.Q().SessionByToken(r.Context(), hashToken(b.RefreshToken))
	if err == nil && s.Kind == KindCLIRefresh {
		_, err = a.store.Q().RevokeSession(r.Context(), db.RevokeSessionParams{ID: s.ID, UserID: s.UserID})
		if err != nil {
			a.fail(w, err, http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
