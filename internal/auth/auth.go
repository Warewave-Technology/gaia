// Package auth signs people in to Gaia and decides what they may do
// (design 11.2-11.4): local accounts with passkeys (break-glass), OIDC,
// opaque session tokens kept as hashes, roles from teams, and the rule
// that a contract one may not see shows its name and nothing more.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// CookieName is the browser session's cookie.
const CookieName = "gaia_session"

// CSRFHeader must be on every request that changes something when it is
// authenticated by the cookie: another site cannot set it without a CORS
// preflight, which Gaia never allows.
const CSRFHeader = "X-Gaia-Request"

// Session kinds.
const (
	KindUI         = "ui"
	KindCLIRefresh = "cli-refresh"
	KindCLIAccess  = "cli-access"
	KindEnroll     = "enroll"
)

// Errors a caller can tell apart.
var (
	ErrUnauthenticated = errors.New("sign in first")
	ErrForbidden       = errors.New("not allowed")
	ErrNoConfig        = errors.New("the catalog has no AuthConfig")
)

// Service is Gaia's sign-in. The catalog gives it AuthConfig and Policy;
// the store keeps people and sessions.
type Service struct {
	store   *store.Store
	catalog func() *catalog.Graph
	log     *slog.Logger
	now     func() time.Time
	limits  limiters
}

// New returns the sign-in service. catalog returns the current catalog.
func New(s *store.Store, catalog func() *catalog.Graph, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{store: s, catalog: catalog, log: log, now: time.Now}
}

func (a *Service) config() (*catalog.AuthConfigSpec, *catalog.Graph, error) {
	g := a.catalog()
	if g == nil {
		return nil, nil, ErrNoConfig
	}
	c := g.AuthConfig()
	if c == nil {
		return nil, g, ErrNoConfig
	}
	return c, g, nil
}

// Principal is who is calling: a person and the session they use.
type Principal struct {
	UserID       string    `json:"id"`
	Username     string    `json:"username"`
	SessionID    string    `json:"session"`
	SessionKind  string    `json:"sessionKind"`
	Teams        []string  `json:"teams"`
	Roles        []string  `json:"roles"`
	StrongAuthAt time.Time `json:"strongAuthAt,omitzero"`
}

// Has reports whether the principal holds role.
func (p *Principal) Has(role string) bool { return p != nil && slices.Contains(p.Roles, role) }

// SeesAll reports whether the principal sees every contract in full.
func (p *Principal) SeesAll() bool { return p.Has("operator") || p.Has("platform-admin") }

type ctxKey struct{}

// From returns the principal of an authenticated request, or nil.
func From(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// With puts a principal into ctx, for handlers and tests.
func With(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// newToken returns a random token and the hash that is stored for it.
func newToken() (token, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// session is a new session to record.
type session struct {
	userID, kind, parent string
	ttl                  time.Duration
	idle                 time.Duration // ui only
	strong               bool          // signed in with a passkey or MFA just now
	r                    *http.Request
}

// newSession records a session and returns its token and ID.
func (a *Service) newSession(ctx context.Context, s session) (token, id string, expires time.Time, err error) {
	token, hash := newToken()
	id = store.NewSessionID()
	now := a.now()
	expires = now.Add(s.ttl)
	p := db.InsertSessionParams{
		ID: id, TokenHash: hash, UserID: s.userID, Kind: s.kind, Parent: text(s.parent),
		ExpiresAt: pgtype.Timestamptz{Time: expires, Valid: true},
	}
	if s.idle > 0 {
		p.IdleSeconds = pgtype.Int4{Int32: int32(s.idle / time.Second), Valid: true} //nolint:gosec // hours, not years
	}
	if s.strong {
		p.StrongAuthAt = pgtype.Timestamptz{Time: now, Valid: true}
	}
	if s.r != nil {
		p.Ip, p.UserAgent = text(clientIP(s.r)), text(trunc(s.r.UserAgent(), 300))
	}
	return token, id, expires, a.store.Q().InsertSession(ctx, p)
}

// uiSession starts a browser session and sets its cookie.
func (a *Service) uiSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID string, strong bool) error {
	cfg, _, err := a.config()
	if err != nil {
		return err
	}
	ttl, idle, _, _ := cfg.Sessions.Durations()
	token, _, expires, err := a.newSession(ctx, session{userID: userID, kind: KindUI, ttl: ttl, idle: idle, strong: strong, r: r})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: strings.HasPrefix(cfg.URL, "https://"), SameSite: http.SameSiteStrictMode,
	})
	return nil
}

// touchEvery limits how often a session's last use is written.
const touchEvery = time.Minute

// Authenticate finds the principal of a request: a CLI access token in the
// Authorization header, or the browser's session cookie.
func (a *Service) Authenticate(r *http.Request) (*Principal, error) {
	ctx := r.Context()
	var token string
	kinds := []string{KindUI}
	if h := r.Header.Get("Authorization"); h != "" {
		t, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return nil, ErrUnauthenticated
		}
		token, kinds = t, []string{KindCLIAccess}
	} else if c, err := r.Cookie(CookieName); err == nil {
		token = c.Value
	}
	if token == "" {
		return nil, ErrUnauthenticated
	}
	s, err := a.store.Q().SessionByToken(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, err
	}
	now := a.now()
	switch {
	case !slices.Contains(kinds, s.Kind), s.RevokedAt.Valid, !s.Enabled, !now.Before(s.ExpiresAt.Time):
		return nil, ErrUnauthenticated
	case s.IdleSeconds.Valid && now.Sub(s.LastSeenAt.Time) > time.Duration(s.IdleSeconds.Int32)*time.Second:
		return nil, ErrUnauthenticated
	}
	if now.Sub(s.LastSeenAt.Time) > touchEvery {
		if err := a.store.Q().TouchSession(ctx, s.ID); err != nil {
			return nil, err
		}
	}
	return a.principal(ctx, s.UserID, s.Username, s.ID, s.Kind, s.StrongAuthAt)
}

func (a *Service) principal(ctx context.Context, userID, username, sessionID, kind string, strong pgtype.Timestamptz) (*Principal, error) {
	teams, err := a.store.Q().Teams(ctx, userID)
	if err != nil {
		return nil, err
	}
	p := &Principal{UserID: userID, Username: username, SessionID: sessionID, SessionKind: kind, Teams: teams, Roles: []string{"viewer"}}
	if g := a.catalog(); g != nil {
		p.Roles = g.RolesOf(teams)
	}
	if strong.Valid {
		p.StrongAuthAt = strong.Time
	}
	if p.Teams == nil {
		p.Teams = []string{}
	}
	return p, nil
}

// Guard lets a request through when it is authenticated and its principal
// holds role ("" for anyone signed in). Requests that change something and
// come with the cookie must carry CSRFHeader.
func (a *Service) Guard(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := a.Authenticate(r)
		switch {
		case errors.Is(err, ErrUnauthenticated):
			problem(w, http.StatusUnauthorized, ErrUnauthenticated.Error())
			return
		case err != nil:
			a.log.Error("auth", "err", err)
			problem(w, http.StatusInternalServerError, "internal error")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && p.SessionKind == KindUI && r.Header.Get(CSRFHeader) != "1" {
			problem(w, http.StatusForbidden, "missing "+CSRFHeader+" header")
			return
		}
		if role != "" && !p.Has(role) {
			problem(w, http.StatusForbidden, fmt.Sprintf("needs the %s role", role))
			return
		}
		h(w, r.WithContext(With(r.Context(), p)))
	}
}

// ContractAccess is what a principal may see of a contract.
type ContractAccess int

const (
	// Name only: the contract exists and that is all (design 11.4).
	Name ContractAccess = iota
	// Full: YAML, servers, logs and records.
	Full
)

// Access returns what p may see of the contract: everything for operators
// and platform admins, everything for the team that owns it, else its name.
func (a *Service) Access(p *Principal, contract string) ContractAccess {
	if p.SeesAll() {
		return Full
	}
	g := a.catalog()
	if g == nil {
		return Name
	}
	o, ok := g.Catalog.Get(catalog.KindServer, contract)
	if !ok {
		return Name
	}
	if owner := o.Metadata.Labels["owner"]; owner != "" && slices.Contains(p.Teams, owner) {
		return Full
	}
	return Name
}

func problem(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// IssueAccessToken gives a person a CLI access token for ttl, without a
// refresh token. CLI sign-in uses it after its own checks; tests use it to
// call the API as someone.
func (a *Service) IssueAccessToken(ctx context.Context, userID string, ttl time.Duration, strong bool) (string, error) {
	token, _, _, err := a.newSession(ctx, session{userID: userID, kind: KindCLIAccess, ttl: ttl, strong: strong})
	return token, err
}
