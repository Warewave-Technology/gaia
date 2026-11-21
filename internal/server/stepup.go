package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Step-up (ADR-008 §4): destructive and security actions need a fresh second factor inside the request, every time.
// The client asks for a step-up for one action and its target (POST /api/v1/step-up with the method and path it will
// send), proves the factor, and sends the action within 120 seconds with the step-up's id (X-Gaia-Step-Up) and, for a
// local user, the proof (X-Gaia-Step-Up-Proof):
//   - a WebAuthn assertion of a challenge that holds the action and its target, or a TOTP code, checked when the
//     action arrives; the step-up is used up then, passed or not;
//   - an OIDC user signs in again at the IdP (prompt=login, max_age=0); the IdP's answer, with a second factor and an
//     auth_time within 120 seconds, passes the step-up, which the action then uses.
//
// API tokens cannot pass a step-up (§9). Every step-up asked, passed or failed is an audit entry.

const stepUpWindow = 120 * time.Second

// Step-up rules of a route.
const (
	stepNone    = iota
	stepAlways  // every request
	stepIfLoose // when the handler finds that it weakens protection (a policy made looser, c.requireStepUp)
)

type stepUp struct {
	id        string
	userID    int64
	sessionID int64
	method    string // the HTTP method and concrete path of the action
	path      string
	kind      string // webauthn, totp or oidc
	created   time.Time
	wa        *webauthn.SessionData
	passedAt  time.Time // passed outside the action's request: at the IdP (oidc), or approved in a browser
	passedBy  string    // how, for the audit log
	asked     StepUpChallenge
}

type stepUps struct {
	mu sync.Mutex
	m  map[string]*stepUp
}

func (p *stepUps) put(su *stepUp) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.m {
		if su.created.Sub(v.created) > 2*stepUpWindow {
			delete(p.m, k)
		}
	}
	p.m[su.id] = su
}

// take removes and returns a step-up: it is used once.
func (p *stepUps) take(id string) *stepUp {
	p.mu.Lock()
	defer p.mu.Unlock()
	su := p.m[id]
	delete(p.m, id)
	return su
}

func (p *stepUps) get(id string) *stepUp {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.m[id]
}

// StepUpRequest asks for a step-up for one action.
type StepUpRequest struct {
	Method string `json:"method"` // the action's HTTP method
	Path   string `json:"path"`   // and its path, e.g. /api/v1/points/42/forget
}

// StepUpChallenge is what the user answers.
type StepUpChallenge struct {
	ID        string   `json:"id"`
	Action    string   `json:"action"` // the route, e.g. POST /api/v1/points/{id}/forget
	Target    string   `json:"target"`
	ExpiresAt string   `json:"expires_at"`
	Factors   []string `json:"factors,omitempty"`      // local: webauthn, totp
	WebAuthn  any      `json:"webauthn,omitempty"`     // the assertion options, the challenge bound to the action
	Redirect  string   `json:"redirect_url,omitempty"` // OIDC: sign in again there
	// A client that cannot answer the factor itself (the CLI and a security key) has it approved in a browser signed in
	// as the same user, at this page, within 120 seconds; then it sends the action with only X-Gaia-Step-Up.
	ApproveURL string `json:"approve_url,omitempty"`
}

// StepUpApprove passes a step-up from another session of the same user (a browser, for the CLI).
type StepUpApprove struct {
	ID string `json:"id"`
	StepUpProof
}

// StepUpProof is X-Gaia-Step-Up-Proof, base64url of this JSON.
type StepUpProof struct {
	Method   string          `json:"method"` // webauthn or totp
	Code     string          `json:"code,omitempty"`
	WebAuthn json.RawMessage `json:"webauthn,omitempty"`
}

// stepUpRoute finds the route an action goes to, if it needs a step-up.
func (s *Server) stepUpRoute(method, path string) *route {
	for i := range s.routes {
		rt := &s.routes[i]
		if rt.stepUp != stepNone && rt.method == method && matchPath(rt.path, path) {
			return rt
		}
	}
	return nil
}

// matchPath matches a concrete path against a route pattern: {x} is one segment.
func matchPath(pattern, path string) bool {
	a, b := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.HasPrefix(a[i], "{") && strings.HasSuffix(a[i], "}") {
			if b[i] == "" {
				return false
			}
			continue
		}
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// stepUpActions lists the routes that need a step-up, for the settings screen.
func stepUpActions(routes []route) []string {
	out := []string{}
	for _, rt := range routes {
		switch rt.stepUp {
		case stepAlways:
			out = append(out, rt.method+" "+rt.path)
		case stepIfLoose:
			out = append(out, rt.method+" "+rt.path+" (when it weakens protection)")
		}
	}
	return out
}

// stepUpChallenge binds a WebAuthn challenge to the action: 16 random bytes and sha256(id, method, path).
func stepUpChallenge(id, method, path string) []byte {
	r := make([]byte, 16)
	rand.Read(r)
	h := sha256.Sum256([]byte(id + "\n" + method + " " + path))
	return append(r, h[:]...)
}

// Step-up refusals, each a 403 with its own code.
var (
	errStepUpRequired = errors.New("step_up_required")
	errStepUpFailed   = errors.New("step_up_failed")
	errIdPUnreachable = errors.New("idp_unreachable")
)

func (s *Server) beginStepUp(c *call) (int, any, error) {
	var in StepUpRequest
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	c.target = in.Method + " " + in.Path
	if c.token != nil {
		return 0, nil, fmt.Errorf("%w: API tokens cannot pass a step-up, so they cannot do this action (ADR-008 §9)", errStepUpFailed)
	}
	rt := s.stepUpRoute(in.Method, in.Path)
	if rt == nil {
		return 0, nil, fmt.Errorf("%w: %s %s needs no step-up", store.ErrInvalid, in.Method, in.Path)
	}
	if !allowed(rt.roles, c.user.Role) {
		return 0, nil, fmt.Errorf("%w: role %s may not %s %s", errForbidden, c.user.Role, rt.method, rt.path)
	}
	now := s.now()
	su := &stepUp{id: auth.NewToken("su_"), userID: c.user.ID, sessionID: c.session.ID, method: in.Method, path: in.Path, created: now}
	out := StepUpChallenge{ID: su.id, Action: rt.method + " " + rt.path, Target: in.Path, ExpiresAt: now.Add(stepUpWindow).UTC().Format(time.RFC3339)}
	if c.user.Source == store.SourceOIDC {
		if c.session.IdPState == idpUnreachable {
			return 0, nil, fmt.Errorf("%w: the identity provider did not answer at the last session refresh, so it cannot check your second factor now; the session goes on until its limit", errIdPUnreachable)
		}
		su.kind = "oidc"
		u, err := s.oidcStepUpURL(ctx, su.id)
		if err != nil {
			return 0, nil, err
		}
		out.Redirect = u
	} else {
		for _, k := range c.user.Factors {
			if k == store.FactorWebAuthn || (k == store.FactorTOTP && c.user.Role != store.RoleAdmin) {
				out.Factors = append(out.Factors, k)
			}
		}
		if len(out.Factors) == 0 {
			return 0, nil, fmt.Errorf("%w: no second factor that can pass a step-up is enrolled", errStepUpFailed)
		}
		if slices.Contains(out.Factors, store.FactorWebAuthn) {
			w, err := s.webauthnUser(ctx, c.user)
			if err != nil {
				return 0, nil, err
			}
			opts, sd, err := s.beginAssertion(w, stepUpChallenge(su.id, in.Method, in.Path))
			if err != nil {
				return 0, nil, err
			}
			su.wa, out.WebAuthn = sd, opts
		}
		out.ApproveURL = s.cfg.PublicURL + "/approve-step-up?id=" + su.id
	}
	su.asked = out
	s.stepups.put(su)
	c.detail = "step-up asked for " + in.Method + " " + in.Path
	return http.StatusOK, out, nil
}

// passStepUp checks the step-up a request carries (X-Gaia-Step-Up). It is used up whether it passes or not, and both
// are audit entries. It returns an error the request is refused with.
func (s *Server) passStepUp(c *call, rt *route) error {
	id := c.r.Header.Get("X-Gaia-Step-Up")
	if id == "" {
		return nil
	}
	ctx := c.r.Context()
	if c.token != nil {
		return fmt.Errorf("%w: API tokens cannot pass a step-up (ADR-008 §9)", errStepUpFailed)
	}
	su := s.stepups.take(id)
	fail := func(why string) error {
		s.auditStep(c, rt, http.StatusForbidden, "step-up failed: "+why)
		return fmt.Errorf("%w: the step-up did not pass: %s", errStepUpFailed, why)
	}
	now := s.now()
	switch {
	case su == nil:
		return fail("unknown or used already (a step-up is for one action)")
	case su.userID != c.user.ID || su.sessionID != c.session.ID:
		return fail("made in another session")
	case su.method != c.r.Method || su.path != c.r.URL.Path:
		return fail(fmt.Sprintf("made for %s %s, not for this action", su.method, su.path))
	case now.Sub(su.created) > stepUpWindow:
		return fail("older than 120 seconds")
	}
	if su.kind == "oidc" || c.r.Header.Get("X-Gaia-Step-Up-Proof") == "" {
		if su.passedAt.IsZero() {
			return fail("not passed: no proof in the request, no new sign-in at the identity provider, no approval in a browser")
		}
		if now.Sub(su.passedAt) > stepUpWindow {
			return fail("passed more than 120 seconds ago")
		}
		c.steppedUp = true
		s.auditStep(c, rt, http.StatusOK, "step-up passed: "+su.passedBy)
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.r.Header.Get("X-Gaia-Step-Up-Proof"))
	var p StepUpProof
	if err != nil || json.Unmarshal(raw, &p) != nil {
		return fail("X-Gaia-Step-Up-Proof is not base64url JSON")
	}
	if p.Method == store.FactorTOTP && c.user.Role == store.RoleAdmin {
		return fail("an admin's step-up is a security key")
	}
	if p.Method != store.FactorTOTP && p.Method != store.FactorWebAuthn {
		return fail("method " + p.Method + ": webauthn or totp")
	}
	what, err := s.checkFactor(ctx, c.user, p.Method, p.Code, p.WebAuthn, su.wa)
	if errors.Is(err, errFactor) {
		if ferr := s.signinFailedFor(c, c.user.Username); ferr != nil {
			return ferr
		}
		return fail(what + ": " + err.Error())
	}
	if err != nil {
		return err
	}
	c.steppedUp = true
	s.auditStep(c, rt, http.StatusOK, "step-up passed: "+what)
	return nil
}

// pendingStepUp shows a step-up of the user's other session to approve (the approval page asks for the same factor).
func (s *Server) pendingStepUp(c *call) (int, any, error) {
	su := s.stepups.get(c.r.PathValue("sid"))
	if su == nil || su.userID != c.user.ID || su.kind == "oidc" || s.now().Sub(su.created) > stepUpWindow {
		return 0, nil, fmt.Errorf("%w: no step-up of yours waits with this id (they last 120 seconds)", store.ErrNotFound)
	}
	out := su.asked
	out.ApproveURL = ""
	return http.StatusOK, out, nil
}

// approveStepUp passes another session's step-up of the same user: the factor is answered here (a browser) for a
// request the other session sends (the CLI).
func (s *Server) approveStepUp(c *call) (int, any, error) {
	var in StepUpApprove
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	if c.token != nil {
		return 0, nil, fmt.Errorf("%w: API tokens cannot pass a step-up (ADR-008 §9)", errStepUpFailed)
	}
	su := s.stepups.get(in.ID)
	if su == nil || su.userID != c.user.ID || su.kind == "oidc" || s.now().Sub(su.created) > stepUpWindow {
		return 0, nil, fmt.Errorf("%w: no step-up of yours waits with this id (they last 120 seconds)", store.ErrNotFound)
	}
	c.target = su.method + " " + su.path
	if in.Method == store.FactorTOTP && c.user.Role == store.RoleAdmin {
		return 0, nil, fmt.Errorf("%w: an admin's step-up is a security key", errStepUpFailed)
	}
	what, err := s.checkFactor(c.r.Context(), c.user, in.Method, in.Code, in.WebAuthn, su.wa)
	if errors.Is(err, errFactor) {
		if ferr := s.signinFailedFor(c, c.user.Username); ferr != nil {
			return 0, nil, ferr
		}
		c.detail = "step-up approval failed: " + what
		return 0, nil, fmt.Errorf("%w: %v", errStepUpFailed, err)
	}
	if err != nil {
		return 0, nil, err
	}
	su.passedAt, su.passedBy = s.now(), fmt.Sprintf("%s, approved in session %d", what, c.session.ID)
	c.detail = fmt.Sprintf("step-up approved for %s %s with %s", su.method, su.path, what)
	return http.StatusNoContent, nil, nil
}

// signinFailedFor counts a failed factor outside a sign-in (a step-up) like one inside (§7).
func (s *Server) signinFailedFor(c *call, username string) error {
	_, _, err := s.db.SigninFailed(c.r.Context(), username, remote(c.r))
	return err
}

func (s *Server) auditStep(c *call, rt *route, outcome int, detail string) {
	e := store.AuditEntry{Username: c.user.Username, Role: c.user.Role, Action: "step_up", Target: rt.method + " " + c.r.URL.Path,
		Outcome: outcome, RemoteAddr: remote(c.r), Detail: detail}
	if c.user.ID != 0 {
		id := c.user.ID
		e.UserID = &id
	}
	if _, err := s.db.AppendAudit(c.r.Context(), e); err != nil {
		s.log.Error("the audit log could not record a step-up", "user", c.user.Username, "error", err)
	}
}

// requireStepUp is for a handler whose action needs a step-up only in some cases (stepIfLoose).
func (c *call) requireStepUp(why string) error {
	if c.steppedUp {
		return nil
	}
	return fmt.Errorf("%w: %s: ask for a step-up for %s %s first", errStepUpRequired, why, c.r.Method, c.r.URL.Path)
}
