package auth

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/time/rate"

	"github.com/Warewave-Technology/gaia/internal/store/db"
)

//go:embed pages
var pages embed.FS

// Route is one endpoint of sign-in. Role "" with Public set means anyone,
// signed in or not.
type Route struct {
	Method, Path string
	Public       bool
	Role         string // "" for anyone signed in
	Handler      http.HandlerFunc
}

// Routes are sign-in's endpoints; the server mounts them next to the API.
func (a *Service) Routes() []Route {
	limited := func(h http.HandlerFunc) http.HandlerFunc { return a.limit(jsonPost(h)) }
	return []Route{
		{Method: "GET", Path: "/login", Public: true, Handler: a.page("pages/login.html")},
		{Method: "GET", Path: "/enroll", Public: true, Handler: a.page("pages/enroll.html")},
		{Method: "GET", Path: "/auth/gaia-auth.js", Public: true, Handler: a.page("pages/gaia-auth.js")},
		{Method: "GET", Path: "/api/v1/auth/config", Public: true, Handler: a.authConfig},
		{Method: "POST", Path: "/api/v1/auth/passkey/begin", Public: true, Handler: limited(a.passkeyBegin)},
		{Method: "POST", Path: "/api/v1/auth/passkey/finish", Public: true, Handler: limited(a.passkeyFinish)},
		{Method: "POST", Path: "/api/v1/auth/enroll/begin", Public: true, Handler: limited(a.enrollBegin)},
		{Method: "POST", Path: "/api/v1/auth/enroll/finish", Public: true, Handler: limited(a.enrollFinish)},
		{Method: "POST", Path: "/api/v1/auth/recovery", Public: true, Handler: limited(a.recovery)},
		{Method: "POST", Path: "/api/v1/auth/password", Public: true, Handler: limited(a.passwordLogin)},
		{Method: "GET", Path: "/auth/oidc/{provider}/start", Public: true, Handler: a.limit(a.oidcStart)},
		{Method: "GET", Path: "/auth/oidc/{provider}/callback", Public: true, Handler: a.limit(a.oidcCallback)},
		{Method: "POST", Path: "/api/v1/auth/oidc/passkey/begin", Public: true, Handler: limited(a.pendingBegin)},
		{Method: "POST", Path: "/api/v1/auth/oidc/passkey/finish", Public: true, Handler: limited(a.pendingFinish)},
		{Method: "POST", Path: "/api/v1/auth/enroll/totp/begin", Public: true, Handler: limited(a.totpBegin)},
		{Method: "POST", Path: "/api/v1/auth/enroll/totp/finish", Public: true, Handler: limited(a.totpFinish)},
		{Method: "POST", Path: "/api/v1/auth/logout", Handler: a.logout},
		{Method: "GET", Path: "/api/v1/me", Handler: a.me},
		{Method: "GET", Path: "/api/v1/me/sessions", Handler: a.mySessions},
		{Method: "DELETE", Path: "/api/v1/me/sessions/{id}", Handler: a.revokeMySession},
		{Method: "GET", Path: "/api/v1/me/passkeys", Handler: a.myPasskeys},
		{Method: "POST", Path: "/api/v1/me/passkeys/begin", Handler: a.strong(a.addPasskeyBegin)},
		{Method: "POST", Path: "/api/v1/me/passkeys/finish", Handler: a.strong(a.addPasskeyFinish)},
		{Method: "DELETE", Path: "/api/v1/me/passkeys/{id}", Handler: a.strong(a.deletePasskey)},
		{Method: "POST", Path: "/api/v1/me/recovery-codes", Handler: a.strong(a.regenerateCodes)},
		{Method: "GET", Path: "/cli-login", Public: true, Handler: a.page("pages/cli-login.html")},
		{Method: "GET", Path: "/device", Public: true, Handler: a.page("pages/device.html")},
		{Method: "POST", Path: "/api/v1/auth/cli/authorize", Handler: a.cliAuthorize},
		{Method: "POST", Path: "/api/v1/auth/cli/token", Public: true, Handler: limited(a.cliToken)},
		{Method: "POST", Path: "/api/v1/auth/cli/refresh", Public: true, Handler: limited(a.cliRefresh)},
		{Method: "POST", Path: "/api/v1/auth/cli/logout", Public: true, Handler: limited(a.cliLogout)},
		{Method: "POST", Path: "/api/v1/auth/device", Public: true, Handler: limited(a.deviceStart)},
		{Method: "POST", Path: "/api/v1/auth/device/token", Public: true, Handler: limited(a.deviceToken)},
		{Method: "GET", Path: "/api/v1/auth/device/{code}", Handler: a.deviceInfo},
		{Method: "POST", Path: "/api/v1/auth/device/{code}/approve", Handler: a.deviceDecide(true)},
		{Method: "POST", Path: "/api/v1/auth/device/{code}/deny", Handler: a.deviceDecide(false)},
		{Method: "POST", Path: "/api/v1/auth/step-up/begin", Handler: a.stepUpBegin},
		{Method: "POST", Path: "/api/v1/auth/step-up/finish", Handler: a.stepUpFinish},
		{Method: "POST", Path: "/api/v1/auth/step-up/totp", Handler: limited(a.stepUpTOTP)},
		{Method: "GET", Path: "/approve/{id}", Public: true, Handler: a.page("pages/approve.html")},
		{Method: "GET", Path: "/api/v1/me/signing-keys", Handler: a.mySigningKeys},
		{Method: "GET", Path: "/api/v1/me/login-keys", Handler: a.myLoginKeys},
		{Method: "POST", Path: "/api/v1/me/login-keys", Handler: a.strong(a.addLoginKey)},
		{Method: "DELETE", Path: "/api/v1/me/login-keys/{fingerprint}", Handler: a.revokeLoginKey},
		{Method: "GET", Path: "/api/v1/me/notifications", Handler: a.myNotifications},
		{Method: "PUT", Path: "/api/v1/me/notifications", Handler: a.setMyNotifications},
		{Method: "POST", Path: "/api/v1/me/signing-keys", Handler: a.strong(a.addSigningKey)},
		{Method: "DELETE", Path: "/api/v1/me/signing-keys/{fingerprint}", Handler: a.strong(a.revokeSigningKey)},
		{Method: "GET", Path: "/api/v1/users", Role: "platform-admin", Handler: a.listUsers},
		{Method: "POST", Path: "/api/v1/users/{id}/disable", Role: "platform-admin", Handler: a.setEnabled(false)},
		{Method: "POST", Path: "/api/v1/users/{id}/enable", Role: "platform-admin", Handler: a.setEnabled(true)},
		{Method: "POST", Path: "/api/v1/users/{id}/enroll", Role: "platform-admin", Handler: a.strong(a.reenroll)},
	}
}

// Mount adds sign-in's endpoints to mux, guarding the private ones.
func (a *Service) Mount(mux *http.ServeMux) {
	for _, rt := range a.Routes() {
		h := rt.Handler
		if !rt.Public {
			h = a.Guard(rt.Role, h)
		}
		mux.HandleFunc(rt.Method+" "+rt.Path, h)
	}
}

func (a *Service) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		b, err := pages.ReadFile(name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		ct := "text/html; charset=utf-8"
		if len(name) > 3 && name[len(name)-3:] == ".js" {
			ct = "text/javascript; charset=utf-8"
		}
		h := w.Header()
		h.Set("Content-Type", ct)
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'unsafe-inline'; img-src 'self' data:; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write(b)
	}
}

// jsonPost refuses what a browser form on another site could send: every
// sign-in call is JSON with CSRFHeader.
func jsonPost(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(CSRFHeader) != "1" || r.Header.Get("Content-Type") != "application/json" {
			problem(w, http.StatusForbidden, "send JSON with the "+CSRFHeader+" header")
			return
		}
		h(w, r)
	}
}

// limit slows down guessing: 20 sign-in calls a minute per address.
func (a *Service) limit(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.limiter(clientIP(r)).Allow() {
			problem(w, http.StatusTooManyRequests, "too many attempts; wait a minute")
			return
		}
		h(w, r)
	}
}

// limiters are per address, per process; replicas each count their own.
type limiters struct {
	mu sync.Mutex
	m  map[string]*rate.Limiter
}

func (a *Service) limiter(ip string) *rate.Limiter {
	a.limits.mu.Lock()
	defer a.limits.mu.Unlock()
	if a.limits.m == nil || len(a.limits.m) > 10000 {
		a.limits.m = map[string]*rate.Limiter{}
	}
	l, ok := a.limits.m[ip]
	if !ok {
		l = rate.NewLimiter(rate.Every(3*time.Second), 20)
		a.limits.m[ip] = l
	}
	return l
}

// strongWindow is how recent a passkey or MFA must be for sensitive
// changes; older sessions step up first.
const strongWindow = 10 * time.Minute

// Strong lets a request through when its passkey or MFA is recent; else it
// answers 403 with stepUp set, and the page confirms with a passkey first.
func (a *Service) Strong(h http.HandlerFunc) http.HandlerFunc { return a.strong(h) }

func (a *Service) strong(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := From(r.Context())
		if p == nil || p.StrongAuthAt.IsZero() || a.now().Sub(p.StrongAuthAt) > strongWindow {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "confirm with your passkey first", "stepUp": true})
			return
		}
		h(w, r)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v); err != nil {
		problem(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return false
	}
	return true
}

func (a *Service) fail(w http.ResponseWriter, err error, status int) {
	if status >= 500 {
		a.log.Error("auth", "err", err)
		problem(w, status, "internal error")
		return
	}
	problem(w, status, err.Error())
}

func (a *Service) authConfig(w http.ResponseWriter, _ *http.Request) {
	cfg, _, err := a.config()
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	type provider struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	out := struct {
		Require   string     `json:"require"`
		Providers []provider `json:"providers"`
	}{Require: cfg.Require, Providers: []provider{}}
	for _, p := range cfg.Providers {
		out.Providers = append(out.Providers, provider{Name: p.Name, Type: p.Type})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Service) passkeyBegin(w http.ResponseWriter, r *http.Request) {
	f, err := a.BeginPasskeyLogin(r.Context())
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

type finishBody struct {
	Flow       string          `json:"flow"`
	Credential json.RawMessage `json:"credential"`
	Name       string          `json:"name,omitempty"`
	Token      string          `json:"token,omitempty"`
}

func (a *Service) passkeyFinish(w http.ResponseWriter, r *http.Request) {
	var b finishBody
	if !decode(w, r, &b) {
		return
	}
	p, err := a.FinishPasskeyLogin(r.Context(), w, r, b.Flow, b.Credential)
	if err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *Service) enrollBegin(w http.ResponseWriter, r *http.Request) {
	var b finishBody
	if !decode(w, r, &b) {
		return
	}
	f, err := a.BeginEnroll(r.Context(), b.Token)
	if err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *Service) enrollFinish(w http.ResponseWriter, r *http.Request) {
	var b finishBody
	if !decode(w, r, &b) {
		return
	}
	codes, err := a.FinishEnroll(r.Context(), w, r, b.Token, b.Flow, b.Name, b.Credential)
	if err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

func (a *Service) recovery(w http.ResponseWriter, r *http.Request) {
	var b struct{ Username, Code string }
	if !decode(w, r, &b) {
		return
	}
	if err := a.RecoveryLogin(r.Context(), w, r, b.Username, b.Code); err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"next": "register a new passkey under Profile"})
}

func (a *Service) logout(w http.ResponseWriter, r *http.Request) {
	p := From(r.Context())
	if _, err := a.store.Q().RevokeSession(r.Context(), db.RevokeSessionParams{ID: p.SessionID, UserID: p.UserID}); err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (a *Service) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, From(r.Context()))
}

// SessionInfo is a session as its owner sees it.
type SessionInfo struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeenAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	IP        string    `json:"ip,omitempty"`
	UserAgent string    `json:"userAgent,omitempty"`
}

func (a *Service) mySessions(w http.ResponseWriter, r *http.Request) {
	p := From(r.Context())
	rows, err := a.store.Q().UserSessions(r.Context(), p.UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	out := []SessionInfo{}
	for _, s := range rows {
		if s.Kind == KindCLIAccess {
			continue // shown through their refresh session
		}
		out = append(out, SessionInfo{
			ID: s.ID, Kind: s.Kind, Current: s.ID == p.SessionID || s.ID == parentOf(r, a, p),
			CreatedAt: s.CreatedAt.Time, LastSeen: s.LastSeenAt.Time, ExpiresAt: s.ExpiresAt.Time,
			IP: s.Ip.String, UserAgent: s.UserAgent.String,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// parentOf is the refresh session of a CLI access token, so that "current"
// marks the CLI session the request came from.
func parentOf(r *http.Request, a *Service, p *Principal) string {
	if p.SessionKind != KindCLIAccess {
		return ""
	}
	s, err := a.store.Q().Session(r.Context(), p.SessionID)
	if err != nil {
		return ""
	}
	return s.Parent.String
}

func (a *Service) revokeMySession(w http.ResponseWriter, r *http.Request) {
	p := From(r.Context())
	n, err := a.store.Q().RevokeSession(r.Context(), db.RevokeSessionParams{ID: r.PathValue("id"), UserID: p.UserID})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if n == 0 {
		problem(w, http.StatusNotFound, "no such session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PasskeyInfo is a passkey as its owner sees it.
type PasskeyInfo struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"createdAt"`
	LastUsed  *time.Time `json:"lastUsedAt,omitempty"`
}

func (a *Service) myPasskeys(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.Q().Credentials(r.Context(), From(r.Context()).UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	out := []PasskeyInfo{}
	for _, c := range rows {
		pi := PasskeyInfo{ID: encodeID(c.ID), Name: c.Name, CreatedAt: c.CreatedAt.Time}
		if c.LastUsedAt.Valid {
			t := c.LastUsedAt.Time
			pi.LastUsed = &t
		}
		out = append(out, pi)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *Service) addPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	f, err := a.beginRegister(r.Context(), From(r.Context()).UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *Service) addPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	var b finishBody
	if !decode(w, r, &b) {
		return
	}
	userID, err := a.finishRegister(r.Context(), b.Flow, b.Name, b.Credential)
	if err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	if userID != From(r.Context()).UserID {
		problem(w, http.StatusForbidden, "this sign-in step belongs to someone else")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Service) deletePasskey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := From(ctx)
	id, err := decodeID(r.PathValue("id"))
	if err != nil {
		problem(w, http.StatusBadRequest, "bad passkey id")
		return
	}
	rows, err := a.store.Q().Credentials(ctx, p.UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if len(rows) == 1 {
		problem(w, http.StatusConflict, "this is your last passkey; add another one first")
		return
	}
	n, err := a.store.Q().DeleteCredential(ctx, db.DeleteCredentialParams{ID: id, UserID: p.UserID})
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	if n == 0 {
		problem(w, http.StatusNotFound, "no such passkey")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Service) regenerateCodes(w http.ResponseWriter, r *http.Request) {
	codes, err := a.newRecoveryCodes(r.Context(), From(r.Context()).UserID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// UserInfo is a person as a platform admin sees them.
type UserInfo struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName,omitempty"`
	Provider    string   `json:"provider"`
	Enabled     bool     `json:"enabled"`
	Teams       []string `json:"teams"`
}

func (a *Service) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := a.store.Q().Users(ctx)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	out := []UserInfo{}
	for _, u := range rows {
		teams, err := a.store.Q().Teams(ctx, u.ID)
		if err != nil {
			a.fail(w, err, http.StatusInternalServerError)
			return
		}
		if teams == nil {
			teams = []string{}
		}
		out = append(out, UserInfo{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName.String, Provider: u.Provider, Enabled: u.Enabled, Teams: teams})
	}
	writeJSON(w, http.StatusOK, out)
}

// setEnabled turns an account on or off; turning it off ends all of its
// sessions at once (design 14.5).
func (a *Service) setEnabled(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		if _, err := a.store.Q().User(ctx, id); errors.Is(err, pgx.ErrNoRows) {
			problem(w, http.StatusNotFound, "no such user")
			return
		}
		if !on && id == From(ctx).UserID {
			problem(w, http.StatusConflict, "you cannot disable yourself")
			return
		}
		err := a.store.Tx(ctx, func(q *db.Queries) error {
			if err := q.SetUserEnabled(ctx, db.SetUserEnabledParams{ID: id, Enabled: on}); err != nil {
				return err
			}
			if !on {
				return q.RevokeUserSessions(ctx, id)
			}
			return nil
		})
		if err != nil {
			a.fail(w, err, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *Service) reenroll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, err := a.store.Q().User(ctx, r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && u.Provider != "local" {
		problem(w, http.StatusNotFound, "no such local user")
		return
	}
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	link, err := a.Enroll(ctx, u.ID)
	if err != nil {
		a.fail(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"enrollUrl": link})
}

func (a *Service) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var b struct{ Username, Password, Code string }
	if !decode(w, r, &b) {
		return
	}
	if err := a.PasswordLogin(r.Context(), w, r, b.Username, b.Password, b.Code); err != nil {
		a.fail(w, err, http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed in"})
}

func (a *Service) totpBegin(w http.ResponseWriter, r *http.Request) {
	var b struct{ Token string }
	if !decode(w, r, &b) {
		return
	}
	setup, err := a.BeginTOTP(r.Context(), b.Token)
	if err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, setup)
}

func (a *Service) totpFinish(w http.ResponseWriter, r *http.Request) {
	var b struct{ Token, Password, Code string }
	if !decode(w, r, &b) {
		return
	}
	codes, err := a.FinishTOTP(r.Context(), w, r, b.Token, b.Password, b.Code)
	if err != nil {
		a.fail(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}
