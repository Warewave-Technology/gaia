package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/Warewave-Technology/gaia/internal/ad"
	"github.com/Warewave-Technology/gaia/internal/audit"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// errLDAP does not say whether the account or the password was wrong.
var errLDAP = errors.New("the username or password is wrong")

// ldapProvider is the AuthConfig's ldap provider by name, or its first.
func (a *Service) ldapProvider(name string) (*catalog.AuthProvider, *catalog.ProviderSpec, error) {
	cfg, g, err := a.config()
	if err != nil {
		return nil, nil, err
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Type != "ldap" || (name != "" && p.Name != name) || p.Directory == nil {
			continue
		}
		o, ok := g.Catalog.Get(catalog.KindProvider, p.Directory.Provider)
		if !ok {
			return nil, nil, errors.New("the directory provider is not in the settings")
		}
		return p, o.Spec.(*catalog.ProviderSpec), nil
	}
	return nil, nil, errors.New("this Gaia does not sign in with a directory")
}

// HasDirectory reports whether people sign in with the directory; local
// accounts are then for break-glass only (design 17.5).
func (a *Service) HasDirectory() bool {
	p, _, err := a.ldapProvider("")
	return err == nil && p != nil
}

// directoryAuth checks a directory password; tests replace it.
var directoryAuth = ad.Authenticate

// ldapLogin checks the person's directory password, then asks for Gaia's
// second factor: a passkey, or a TOTP code under require: mfa. A first
// sign-in registers one (design 17.5).
func (a *Service) ldapLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var b struct{ Provider, Username, Password, Next string }
	if !decode(w, r, &b) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(b.Username))
	prov, ps, err := a.ldapProvider(b.Provider)
	if err != nil {
		a.fail(w, err, http.StatusNotFound)
		return
	}
	sec := map[string]string{}
	if ps.Credentials != "" {
		if a.secrets == nil {
			a.fail(w, errors.New("the directory's credentials are in OpenBao, which this server cannot read"), http.StatusServiceUnavailable)
			return
		}
		if sec, err = a.secrets.Read(ctx, ps.Credentials); err != nil {
			a.fail(w, err, http.StatusBadGateway)
			return
		}
	}
	u, err := directoryAuth(ctx, ad.ConfigFrom(ps.Endpoint, ps.BaseDN, sec), name, b.Password)
	switch {
	case errors.Is(err, ad.ErrBadCredentials):
		a.audit(ctx, r, audit.Entry{Actor: name, Action: "auth.login", Result: "denied", Detail: map[string]string{"method": "ldap", "why": "credentials"}})
		a.fail(w, errLDAP, http.StatusUnauthorized)
		return
	case errors.Is(err, ad.ErrDisabled):
		a.audit(ctx, r, audit.Entry{Actor: name, Action: "auth.login", Result: "denied", Detail: map[string]string{"method": "ldap", "why": "disabled in the directory"}})
		a.fail(w, errors.New("this account is disabled in the directory"), http.StatusForbidden)
		return
	case err != nil:
		a.log.Error("directory sign-in", "err", err)
		a.fail(w, errors.New("the directory did not answer; try again or use a break-glass account"), http.StatusBadGateway)
		return
	}
	userID, err := a.directoryUser(ctx, prov.Directory.Provider, u.Username)
	if err != nil {
		a.audit(ctx, r, audit.Entry{Actor: name, Action: "auth.login", Result: "denied", Detail: map[string]string{"method": "ldap", "why": err.Error()}})
		a.fail(w, err, http.StatusForbidden)
		return
	}
	pending, err := a.saveFlow(ctx, "oidc-pending", userID, map[string]string{"next": safeNext(b.Next), "method": "ldap"})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	cfg, _, _ := a.config()
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: pending, Path: "/", MaxAge: int(flowTTL / time.Second),
		HttpOnly: true, Secure: strings.HasPrefix(cfg.URL, "https://"), SameSite: http.SameSiteStrictMode})
	st, err := a.secondFactor(ctx, userID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// SecondFactor is what a waiting sign-in can finish with.
type SecondFactor struct {
	Require  string `json:"require"` // passkey | mfa
	Username string `json:"username"`
	Passkey  bool   `json:"passkey"` // has one
	TOTP     bool   `json:"totp"`    // has one
}

func (a *Service) secondFactor(ctx context.Context, userID string) (*SecondFactor, error) {
	cfg, _, err := a.config()
	if err != nil {
		return nil, err
	}
	u, err := a.webauthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := &SecondFactor{Require: cfg.Require, Username: u.name, Passkey: len(u.creds) > 0}
	if row, err := a.store.Q().TOTP(ctx, userID); err == nil && row.ConfirmedAt.Valid {
		out.TOTP = true
	}
	return out, nil
}

// pendingState says how a waiting sign-in may finish.
func (a *Service) pendingState(w http.ResponseWriter, r *http.Request) {
	userID, _, err := a.pending(r)
	if err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	st, err := a.secondFactor(r.Context(), userID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// finishPending ends a waiting sign-in with a session.
func (a *Service) finishPending(w http.ResponseWriter, r *http.Request, userID, next string, codes []string) {
	ctx := r.Context()
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

// pendingTOTP finishes a waiting sign-in with a TOTP code.
func (a *Service) pendingTOTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, next, err := a.pending(r)
	if err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	var b struct{ Code string }
	if !decode(w, r, &b) {
		return
	}
	if err := a.requireMFA(); err != nil {
		a.fail(w, err, http.StatusForbidden)
		return
	}
	if row, err := a.store.Q().TOTP(ctx, userID); err != nil || !row.ConfirmedAt.Valid {
		a.fail(w, errors.New("this account has no TOTP yet"), http.StatusConflict)
		return
	}
	if err := a.checkTOTP(ctx, userID, b.Code, false); err != nil {
		a.audit(ctx, r, audit.Entry{ActorID: userID, Action: "auth.login", Result: "denied", Detail: map[string]string{"method": "ldap+totp", "why": "code"}})
		a.fail(w, errors.New("the code is wrong"), http.StatusUnauthorized)
		return
	}
	a.finishPending(w, r, userID, next, nil)
}

// pendingTOTPBegin starts a first sign-in's TOTP: only for an account
// that has no second factor yet.
func (a *Service) pendingTOTPBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, _, err := a.pending(r)
	if err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	if err := a.requireMFA(); err != nil {
		a.fail(w, err, http.StatusForbidden)
		return
	}
	st, err := a.secondFactor(ctx, userID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if st.Passkey || st.TOTP {
		a.fail(w, errors.New("this account has a second factor; sign in with it"), http.StatusConflict)
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Gaia", AccountName: st.Username, Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	ct, err := a.transit.Encrypt(ctx, TransitKey, []byte(key.Secret()))
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := a.store.Q().SetTOTP(ctx, db.SetTOTPParams{UserID: userID, Ciphertext: ct}); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, TOTPSetup{Secret: key.Secret(), URI: key.URL()})
}

// pendingTOTPFinish confirms a first sign-in's TOTP with a code and signs
// the person in, with their recovery codes.
func (a *Service) pendingTOTPFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, next, err := a.pending(r)
	if err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	var b struct{ Code string }
	if !decode(w, r, &b) {
		return
	}
	if err := a.requireMFA(); err != nil {
		a.fail(w, err, http.StatusForbidden)
		return
	}
	row, err := a.store.Q().TOTP(ctx, userID)
	if err != nil || row.ConfirmedAt.Valid {
		a.fail(w, errors.New("start the TOTP setup again"), http.StatusConflict)
		return
	}
	if err := a.checkTOTP(ctx, userID, b.Code, true); err != nil {
		a.fail(w, errors.New("the code is wrong"), http.StatusUnauthorized)
		return
	}
	if err := a.store.Q().ConfirmTOTP(ctx, userID); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	_ = a.checkTOTP(ctx, userID, b.Code, false) // the code is used up
	codes, err := a.newRecoveryCodes(ctx, userID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	a.audit(ctx, r, audit.Entry{ActorID: userID, Action: "auth.totp.enroll"})
	a.finishPending(w, r, userID, next, codes)
}

// record writes an entry for what the signed-in caller did.
func (a *Service) record(r *http.Request, action, object string, detail any) {
	e := audit.Entry{Action: action, Object: object, Detail: detail}
	if p := From(r.Context()); p != nil {
		e.Actor, e.ActorID, e.SessionID = p.Username, p.UserID, p.SessionID
	}
	a.audit(r.Context(), r, e)
}

// audit writes an audit entry for a sign-in; the person is named by the
// entry, or looked up by its ActorID. A failure to write is logged, not
// shown: the sign-in itself is decided by its own checks.
func (a *Service) audit(ctx context.Context, r *http.Request, e audit.Entry) {
	if e.Actor == "" && e.ActorID != "" {
		if u, err := a.store.Q().User(ctx, e.ActorID); err == nil {
			e.Actor = u.Username
		}
	}
	if e.Actor == "" {
		e.Actor = "anonymous"
	}
	if r != nil {
		e.Address = clientIP(r)
		if p := From(r.Context()); p != nil && e.SessionID == "" {
			e.SessionID = p.SessionID
		}
	}
	if err := audit.Write(ctx, a.store.Q(), e); err != nil {
		a.log.Error("audit", "action", e.Action, "err", err)
	}
}
