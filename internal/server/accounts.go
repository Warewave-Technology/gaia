package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// One's own account (factors, sessions, password), the admins' view of sessions and API tokens, and the CLI's sign-in
// through a browser (ADR-008 §3, §5, §9).

// Me is the signed-in user with what the screens need about the session.
type Me struct {
	store.User
	Session           store.Session `json:"session"`
	PasswordExpiresAt string        `json:"password_expires_at,omitempty"`
	PasswordSoon      bool          `json:"password_expires_soon"` // within 14 days: remind (§6)
	IdleTimeoutS      int           `json:"idle_timeout_s"`
	ViaToken          string        `json:"api_token,omitempty"` // the name of the API token the request came with
}

func (s *Server) me(c *call) (int, any, error) {
	st, err := s.settings(c.r.Context())
	if err != nil {
		return 0, nil, err
	}
	out := Me{User: c.user, Session: c.session, IdleTimeoutS: int(st.idle() / time.Second)}
	if c.token != nil {
		out.ViaToken = c.token.Name
	}
	if exp := st.passwordExpiresAt(c.user); !exp.IsZero() {
		out.PasswordExpiresAt = exp.UTC().Format(time.RFC3339)
		out.PasswordSoon = exp.Sub(s.now()) < PasswordReminder
	}
	return http.StatusOK, out, nil
}

// PasswordChange changes one's own password (with a step-up; the current password is asked for too).
type PasswordChange struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) changePassword(c *call) (int, any, error) {
	var in PasswordChange
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	if c.user.Source != store.SourceLocal {
		return 0, nil, fmt.Errorf("%w: you sign in at the identity provider: change the password there", store.ErrInvalid)
	}
	if _, err := s.db.Authenticate(ctx, c.user.Username, in.CurrentPassword); errors.Is(err, store.ErrBadLogin) {
		if err := s.signinFailed(c, c.user.Username, "wrong current password"); err != nil {
			return 0, nil, err
		}
		return 0, nil, fmt.Errorf("%w: the current password is wrong", store.ErrInvalid)
	} else if err != nil {
		return 0, nil, err
	}
	if err := s.checkBreach(ctx, in.NewPassword); err != nil {
		return 0, nil, err
	}
	if _, err := s.db.ChangeOwnPassword(ctx, c.user.ID, in.NewPassword, c.session.ID); err != nil {
		return 0, nil, err
	}
	c.detail = "password changed; other sessions ended"
	return http.StatusNoContent, nil, nil
}

// ownEnrolments are enrolments of a factor on one's own account, between begin and finish (bound to the session).
type ownEnrolments struct {
	mu sync.Mutex
	m  map[int64]*pendingSignin // by session ID
}

func (s *Server) myFactorBegin(c *call) (int, any, error) {
	var in EnrolBegin
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	if c.user.Source != store.SourceLocal {
		return 0, nil, fmt.Errorf("%w: your second factor is the identity provider's", store.ErrInvalid)
	}
	ps := &pendingSignin{userID: c.user.ID, expires: s.now().Add(signinStepFor)}
	ch, err := s.enrolBegin(c.r.Context(), c.user, in.Kind, ps)
	if err != nil {
		return 0, nil, err
	}
	s.enrolling.mu.Lock()
	s.enrolling.m[c.session.ID] = ps
	s.enrolling.mu.Unlock()
	c.detail = "enrolment of a " + in.Kind + " started"
	return http.StatusOK, ch, nil
}

func (s *Server) myFactorFinish(c *call) (int, any, error) {
	var in EnrolFinish
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	s.enrolling.mu.Lock()
	ps := s.enrolling.m[c.session.ID]
	delete(s.enrolling.m, c.session.ID)
	s.enrolling.mu.Unlock()
	if ps == nil || s.now().After(ps.expires) {
		return 0, nil, fmt.Errorf("%w: no enrolment was started in this session within 5 minutes", store.ErrInvalid)
	}
	f, err := s.enrolFinish(c.r.Context(), c.user.Username, c.user, in, ps, c.session.ID)
	if err != nil {
		return 0, nil, err
	}
	c.detail = fmt.Sprintf("enrolled %s %q; other sessions ended", f.Kind, f.Name)
	return http.StatusCreated, f, nil
}

func (s *Server) myFactorRemove(c *call) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	if err := s.db.RemoveFactor(c.r.Context(), c.user.Username, c.user.ID, id, c.session.ID); err != nil {
		return 0, nil, err
	}
	c.detail = fmt.Sprintf("factor %d removed; other sessions ended", id)
	return http.StatusNoContent, nil, nil
}

// RecoveryCodes are new recovery codes, shown once.
type RecoveryCodes struct {
	Codes []string `json:"codes"`
}

func (s *Server) myRecoveryCodes(c *call) (int, any, error) {
	if c.user.Source != store.SourceLocal {
		return 0, nil, fmt.Errorf("%w: your second factor is the identity provider's", store.ErrInvalid)
	}
	codes, err := s.newRecoveryCodes(c.r.Context(), c.user)
	if err != nil {
		return 0, nil, err
	}
	c.detail = fmt.Sprintf("%d new recovery codes; the old ones stopped working", len(codes))
	return http.StatusOK, RecoveryCodes{Codes: codes}, nil
}

func (s *Server) endSession(c *call, own bool) (int, any, error) {
	id, err := c.id("id")
	if err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	ss, err := s.db.SessionByID(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	if own && ss.UserID != c.user.ID {
		return 0, nil, fmt.Errorf("%w: session %d", store.ErrNotFound, id)
	}
	c.target = fmt.Sprintf("session:%d", id)
	if err := s.db.EndSession(ctx, id, store.EndRevoked, c.user.Username); err != nil {
		return 0, nil, err
	}
	c.detail = fmt.Sprintf("signed out session %d of %s", id, ss.Username)
	return http.StatusNoContent, nil, nil
}

// SessionsEnded counts sessions ended.
type SessionsEnded struct {
	Ended int `json:"ended"`
}

// endAllButMine signs everyone out but this session ("Sign out everyone but me").
func (s *Server) endAllButMine(c *call) (int, any, error) {
	ctx := c.r.Context()
	ss, err := s.db.Sessions(ctx, 0)
	if err != nil {
		return 0, nil, err
	}
	n := 0
	for _, x := range ss {
		if x.ID == c.session.ID {
			continue
		}
		if err := s.db.EndSession(ctx, x.ID, store.EndRevoked, c.user.Username); err != nil {
			return 0, nil, err
		}
		n++
	}
	c.detail = fmt.Sprintf("signed out %d sessions", n)
	return http.StatusOK, SessionsEnded{Ended: n}, nil
}

// NewToken is an API token with its secret, shown once.
type NewToken struct {
	Token string         `json:"token"`
	Info  store.APIToken `json:"info"`
}

func (s *Server) createToken(c *call) (int, any, error) {
	var in store.NewAPIToken
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	tok, t, err := s.db.CreateAPIToken(c.r.Context(), c.user.Username, in)
	if err != nil {
		return 0, nil, err
	}
	c.target = "api_token:" + t.Name
	c.detail = fmt.Sprintf("API token %s made, role %s, until %s", t.Name, t.Role, t.ExpiresAt)
	return http.StatusCreated, NewToken{Token: tok, Info: t}, nil
}

// --- the CLI's sign-in through a browser ---

// A local user whose only factor is a security key (every admin) cannot answer WebAuthn in a terminal. `gaia login
// -browser` asks for a code; the user opens the page it names, signed in, and approves with a step-up; the CLI then
// gets a session of its own for that user. The same as the device grant (RFC 8628), with Gaia as the authorization
// server.

const cliApprovalFor = 10 * time.Minute

type cliApproval struct {
	code, deviceID, addr, userAgent string
	created                         time.Time
	approvedBy                      int64  // the user who approved (0: not yet)
	method                          string // how the approver's step-up passed
}

type cliApprovals struct {
	mu sync.Mutex
	m  map[string]*cliApproval // by the handle's sha256
}

// CLIApprovalStart starts a CLI sign-in through a browser.
type CLIApprovalStart struct {
	DeviceID string `json:"device_id,omitempty"`
}

// CLIApprove approves a CLI sign-in in the browser.
type CLIApprove struct {
	UserCode string `json:"user_code"`
}

func userCode() string {
	const letters = "BCDFGHJKLMNPQRSTVWXZ" // no vowels: no words; 20^8 codes
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func (s *Server) cliApprovalStart(c *call) (int, any, error) {
	var in CLIApprovalStart
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	now := s.now()
	a := &cliApproval{code: userCode(), deviceID: in.DeviceID, addr: remote(c.r), userAgent: c.r.UserAgent(), created: now}
	h := auth.NewToken("cl_")
	s.approvals.mu.Lock()
	for k, v := range s.approvals.m {
		if now.Sub(v.created) > cliApprovalFor {
			delete(s.approvals.m, k)
		}
	}
	s.approvals.m[auth.SHA256Hex(h)] = a
	s.approvals.mu.Unlock()
	c.detail = "CLI sign-in through a browser started from " + a.addr
	return http.StatusOK, DeviceChallenge{Handle: h, UserCode: a.code, VerificationURI: s.cfg.PublicURL + "/approve-cli",
		VerificationURIComplete: s.cfg.PublicURL + "/approve-cli?code=" + a.code, ExpiresAt: now.Add(cliApprovalFor).UTC().Format(time.RFC3339), IntervalS: 3}, nil
}

func (s *Server) cliApprove(c *call) (int, any, error) {
	var in CLIApprove
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	if c.user.Source != store.SourceLocal {
		return 0, nil, fmt.Errorf("%w: you sign in at the identity provider: use gaia login -sso", store.ErrInvalid)
	}
	code := strings.ToUpper(strings.TrimSpace(in.UserCode))
	now := s.now()
	s.approvals.mu.Lock()
	defer s.approvals.mu.Unlock()
	for _, a := range s.approvals.m {
		if a.code == code && now.Sub(a.created) <= cliApprovalFor && a.approvedBy == 0 {
			a.approvedBy, a.method = c.user.ID, c.session.Method
			c.detail = fmt.Sprintf("approved the CLI sign-in %s from %s", code, a.addr)
			return http.StatusNoContent, nil, nil
		}
	}
	return 0, nil, fmt.Errorf("%w: no CLI sign-in waits with code %s (codes last 10 minutes)", store.ErrNotFound, code)
}

func (s *Server) cliApprovalToken(c *call) (int, any, error) {
	var in DevicePoll
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	key := auth.SHA256Hex(in.Handle)
	now := s.now()
	s.approvals.mu.Lock()
	a := s.approvals.m[key]
	if a != nil && a.approvedBy != 0 {
		delete(s.approvals.m, key)
	}
	s.approvals.mu.Unlock()
	if a == nil || now.Sub(a.created) > cliApprovalFor {
		return 0, nil, fmt.Errorf("%w: the CLI sign-in expired or is unknown: start again", errUnauthorized)
	}
	if a.approvedBy == 0 {
		return http.StatusOK, SigninStep{Stage: "pending"}, nil
	}
	u, err := s.db.User(ctx, a.approvedBy)
	if err != nil {
		return 0, nil, err
	}
	st, err := s.settings(ctx)
	if err != nil {
		return 0, nil, err
	}
	c.target = "user:" + u.Username
	c.detail = "CLI sign-in approved in a browser"
	ps := &pendingSignin{userID: u.ID, username: u.Username, kind: "cli", deviceID: a.deviceID, userAgent: a.userAgent, addr: a.addr,
		method: a.method, breakGlass: st.OIDC.Enabled}
	return s.startSession(c, ps, u, st, "")
}
