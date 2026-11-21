package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Sign-in with a local account (ADR-008 §1, §3, §6, §7). It has steps, and a session exists only after the last:
//
//	password -> enrol a second factor (first sign-in, or after an admin's reset) | the second factor
//	         -> a new password (expired, reset by an admin, or found on the blocklist) -> session
//
// Between steps the client holds a sign-in token (5 minutes, in memory: a restart starts the sign-in again). Each
// failed step (password, factor, recovery code) counts for the account and the address (§7).

const signinStepFor = 5 * time.Minute

// Stages of a sign-in.
const (
	stageFactor   = "second_factor"
	stageEnrol    = "enrol"
	stagePassword = "change_password"
	stageDone     = "done"
)

type pendingSignin struct {
	userID     int64
	username   string
	kind       string // browser or cli
	deviceID   string
	userAgent  string
	addr       string
	stage      string
	method     string // the factor that passed
	breakGlass bool
	why        string // stagePassword: why a new password is needed
	expires    time.Time
	wa         *webauthn.SessionData // the open WebAuthn ceremony
	totp       []byte                // enrolment: the secret shown
	codes      []string              // recovery codes made at enrolment, shown when the sign-in ends
}

type signins struct {
	mu sync.Mutex
	m  map[string]*pendingSignin // by the token's sha256
}

func (p *signins) put(ps *pendingSignin, now time.Time) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.m {
		if now.After(v.expires) {
			delete(p.m, k)
		}
	}
	tok := auth.NewToken("gaias_")
	ps.expires = now.Add(signinStepFor)
	p.m[auth.SHA256Hex(tok)] = ps
	return tok
}

// take returns the sign-in of a token, or an error the client shows ("start again").
func (p *signins) get(tok string, now time.Time) (*pendingSignin, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ps := p.m[auth.SHA256Hex(tok)]
	if ps == nil || now.After(ps.expires) {
		return nil, fmt.Errorf("%w: the sign-in expired or is unknown: start again with the password", errUnauthorized)
	}
	return ps, nil
}

func (p *signins) drop(tok string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, auth.SHA256Hex(tok))
}

// LoginRequest starts a sign-in with a local account.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Browser: the session goes into an HttpOnly cookie (__Host-gaia_session) and not into the answer, so the web UI's
	// scripts never hold it (T20).
	Browser  bool   `json:"browser,omitempty"`
	DeviceID string `json:"device_id,omitempty"` // the CLI's device id (a browser's is its __Host-gaia_device cookie)
}

// SigninStep is the answer to every step of a sign-in: what comes next, or the session.
type SigninStep struct {
	Stage       string `json:"stage"` // second_factor, enrol, change_password, done
	SigninToken string `json:"signin_token,omitempty"`
	Username    string `json:"username"`
	// second_factor: what the user may answer with (webauthn, totp, recovery_code), and the WebAuthn challenge.
	Factors  []string `json:"factors,omitempty"`
	WebAuthn any      `json:"webauthn,omitempty"`
	// enrol: the kinds the user may enrol (an admin: webauthn only).
	Enrol []string `json:"enrol,omitempty"`
	// change_password: why (admin_reset, blocklist, expired).
	PasswordChange string `json:"password_change,omitempty"`
	// done.
	Token          string      `json:"token,omitempty"` // a CLI session's token (a browser's is in the cookie)
	AbsoluteAt     string      `json:"absolute_at,omitempty"`
	IdleTimeoutS   int         `json:"idle_timeout_s,omitempty"`
	User           *store.User `json:"user,omitempty"`
	NewDevice      bool        `json:"new_device,omitempty"`
	RecoveryCodes  []string    `json:"recovery_codes,omitempty"` // shown once, after the first enrolment
	PasswordExpiry string      `json:"password_expires_at,omitempty"`
}

// FactorAnswer answers the second-factor step.
type FactorAnswer struct {
	SigninToken string          `json:"signin_token"`
	Method      string          `json:"method"`             // webauthn, totp or recovery_code
	Code        string          `json:"code,omitempty"`     // totp, recovery_code
	WebAuthn    json.RawMessage `json:"webauthn,omitempty"` // the browser's PublicKeyCredential, as JSON
}

// EnrolBegin starts enrolling a second factor during sign-in (and, with a session, on one's own account).
type EnrolBegin struct {
	SigninToken string `json:"signin_token,omitempty"`
	Kind        string `json:"kind"` // webauthn or totp
	Name        string `json:"name,omitempty"`
}

// EnrolChallenge is what the user's authenticator needs: WebAuthn's creation options, or the TOTP secret.
type EnrolChallenge struct {
	Kind       string `json:"kind"`
	WebAuthn   any    `json:"webauthn,omitempty"`
	TOTPSecret string `json:"totp_secret,omitempty"` // base32, to type in
	TOTPURI    string `json:"totp_uri,omitempty"`    // otpauth://, for a QR code
}

// EnrolFinish ends an enrolment: the authenticator's answer.
type EnrolFinish struct {
	SigninToken string          `json:"signin_token,omitempty"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name,omitempty"`
	Code        string          `json:"code,omitempty"`     // totp: a code from the app
	WebAuthn    json.RawMessage `json:"webauthn,omitempty"` // webauthn: the browser's PublicKeyCredential
}

// NewPassword is the change-password step.
type NewPassword struct {
	SigninToken string `json:"signin_token"`
	NewPassword string `json:"new_password"`
}

// signinWait is when the next attempt for a username from an address may be made (zero: now), and why (§7): an
// account paused after 10 failures within 15 minutes, or a delay that doubles from the 3rd failure.
func (s *Server) signinWait(ctx context.Context, username, addr string) (time.Time, string, error) {
	now := s.now()
	f, err := s.db.SigninFailures(ctx, username, addr)
	if err != nil {
		return time.Time{}, "", err
	}
	if u, err := s.db.UserByName(ctx, username); err == nil {
		if locked, until := u.Locked(now); locked {
			return until, fmt.Sprintf("sign-in for this account is paused after %d failed attempts", store.FailuresToPause), nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return time.Time{}, "", err
	}
	if f.ByUser >= store.FailuresToPause {
		if until := f.LastUser.Add(store.PauseFor); now.Before(until) {
			return until, fmt.Sprintf("sign-in for this account is paused after %d failed attempts", store.FailuresToPause), nil
		}
	}
	wait := func(n int, last time.Time) time.Time {
		if n <= store.FailuresBeforeWait {
			return time.Time{}
		}
		d := min(time.Second<<min(n-store.FailuresBeforeWait-1, 20), store.PauseFor)
		return last.Add(d)
	}
	for _, w := range []struct {
		until time.Time
		why   string
	}{{wait(f.ByUser, f.LastUser), fmt.Sprintf("%d failed attempts for this account", f.ByUser)},
		{wait(f.ByAddr, f.LastAddr), fmt.Sprintf("%d failed attempts from %s", f.ByAddr, addr)}} {
		if now.Before(w.until) {
			return w.until, w.why, nil
		}
	}
	return time.Time{}, "", nil
}

// checkWait refuses an attempt that comes before its wait is over: 429 with Retry-After, not checked, not counted.
func (s *Server) checkWait(c *call, username string) error {
	until, why, err := s.signinWait(c.r.Context(), username, remote(c.r))
	if err != nil || until.IsZero() {
		return err
	}
	return refuse(c, until, s.now(), why)
}

// refuse answers a refused attempt: 429 with Retry-After.
func refuse(c *call, until, now time.Time, why string) error {
	secs := int(until.Sub(now).Seconds() + 0.999)
	c.w.Header().Set("Retry-After", fmt.Sprint(max(secs, 1)))
	c.detail = "sign-in refused: " + why
	return fmt.Errorf("%w: %s; try again after %s", errTooManyLogins, why, until.UTC().Format(time.RFC3339))
}

// signinFailed counts a failed step for the username and the address; the 10th within 15 minutes pauses the account,
// which notifies admins (account_locked).
func (s *Server) signinFailed(c *call, username, what string) error {
	paused, until, err := s.db.SigninFailed(c.r.Context(), username, remote(c.r))
	if err != nil {
		return err
	}
	c.detail = "sign-in failed: " + what
	if paused {
		c.detail += "; account paused until " + until.UTC().Format(time.RFC3339)
	}
	return nil
}

var errSignin = errors.New("sign-in refused") // 403 with a reason: the password was right, but the account may not sign in

func (s *Server) login(c *call) (int, any, error) {
	var in LoginRequest
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	c.target = "user:" + in.Username
	if err := s.checkWait(c, in.Username); err != nil {
		return 0, nil, err
	}
	u, err := s.db.Authenticate(ctx, in.Username, in.Password)
	if errors.Is(err, store.ErrBadLogin) {
		if err := s.signinFailed(c, in.Username, "wrong password"); err != nil {
			return 0, nil, err
		}
		return 0, nil, fmt.Errorf("%w: %v", errUnauthorized, store.ErrBadLogin)
	}
	if err != nil {
		return 0, nil, err
	}
	st, err := s.settings(ctx)
	if err != nil {
		return 0, nil, err
	}
	now := s.now()
	switch {
	case st.OIDC.Enabled && !u.BreakGlass:
		c.detail = "local sign-in refused: single sign-on is on and the user is not break-glass"
		return 0, nil, fmt.Errorf("%w: sign in with %s: while it is on, only break-glass admins use a local account", errSignin, st.OIDC.Name)
	case u.Expired(now):
		c.detail = "sign-in refused: the account expired at " + u.ExpiresAt
		return 0, nil, fmt.Errorf("%w: the account expired at %s: an admin can renew it", errSignin, u.ExpiresAt)
	}
	// A password on a newer blocklist must change (§6): only now is it known in clear.
	if !u.BlocklistChecked() && u.PasswordChange == "" {
		listed, err := auth.Blocklisted(in.Password)
		if err != nil {
			return 0, nil, err
		}
		if listed {
			if err := s.db.MustChangePassword(ctx, u.ID, store.PasswordChangeBlocklist); err != nil {
				return 0, nil, err
			}
			u.PasswordChange = store.PasswordChangeBlocklist
		} else if err := s.db.NoteBlocklistChecked(ctx, u.ID); err != nil {
			return 0, nil, err
		}
	}
	ps := &pendingSignin{userID: u.ID, username: u.Username, kind: "cli", deviceID: in.DeviceID, userAgent: c.r.UserAgent(),
		addr: remote(c.r), breakGlass: st.OIDC.Enabled}
	if in.Browser {
		ps.kind, ps.deviceID = "browser", s.deviceCookie(c)
	}
	c.detail = "password accepted"
	if len(u.Factors) == 0 {
		ps.stage = stageEnrol
		c.detail += "; no second factor yet: enrolment"
		return 200, SigninStep{Stage: stageEnrol, SigninToken: s.signins.put(ps, now), Username: u.Username, Enrol: enrolKinds(u)}, nil
	}
	ps.stage = stageFactor
	return s.factorStep(ctx, c, ps, u)
}

// enrolKinds is what a user may enrol: an admin a security key only (§3).
func enrolKinds(u store.User) []string {
	if u.Role == store.RoleAdmin {
		return []string{store.FactorWebAuthn}
	}
	return []string{store.FactorWebAuthn, store.FactorTOTP}
}

// factorStep asks for the second factor.
func (s *Server) factorStep(ctx context.Context, c *call, ps *pendingSignin, u store.User) (int, any, error) {
	out := SigninStep{Stage: stageFactor, Username: u.Username}
	for _, k := range u.Factors {
		out.Factors = append(out.Factors, k)
	}
	if left, err := s.db.RecoveryCodesLeft(ctx, u.ID); err != nil {
		return 0, nil, err
	} else if left > 0 {
		out.Factors = append(out.Factors, "recovery_code")
	}
	if contains(u.Factors, store.FactorWebAuthn) && s.webauthnReady() != nil {
		// The key cannot be asked for: another factor, or a recovery code, signs the user in.
		out.Factors = slices.DeleteFunc(out.Factors, func(k string) bool { return k == store.FactorWebAuthn })
		if len(out.Factors) == 0 {
			return 0, nil, s.webauthnReady()
		}
		c.detail += "; security keys unavailable: " + s.webauthnReady().Error()
	} else if contains(u.Factors, store.FactorWebAuthn) {
		w, err := s.webauthnUser(ctx, u)
		if err != nil {
			return 0, nil, err
		}
		opts, sd, err := s.beginAssertion(w, nil)
		if err != nil {
			return 0, nil, err
		}
		ps.wa, out.WebAuthn = sd, opts
	}
	out.SigninToken = s.signins.put(ps, s.now())
	return 200, out, nil
}

func (s *Server) loginFactor(c *call) (int, any, error) {
	var in FactorAnswer
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	ps, err := s.signins.get(in.SigninToken, s.now())
	if err != nil {
		return 0, nil, err
	}
	c.target = "user:" + ps.username
	if ps.stage != stageFactor {
		return 0, nil, fmt.Errorf("%w: this sign-in is at %s, not second_factor", store.ErrInvalid, ps.stage)
	}
	if err := s.checkWait(c, ps.username); err != nil {
		return 0, nil, err
	}
	u, err := s.db.User(ctx, ps.userID)
	if err != nil {
		return 0, nil, err
	}
	what, err := s.checkFactor(ctx, u, in.Method, in.Code, in.WebAuthn, ps.wa)
	if errors.Is(err, errFactor) {
		s.signins.drop(in.SigninToken) // a wrong factor starts the sign-in again
		if ferr := s.signinFailed(c, ps.username, what+": "+err.Error()); ferr != nil {
			return 0, nil, ferr
		}
		return 0, nil, fmt.Errorf("%w: %v", errUnauthorized, err)
	}
	if err != nil {
		return 0, nil, err
	}
	ps.method = in.Method
	c.detail = what + " accepted"
	s.signins.drop(in.SigninToken)
	return s.afterFactor(c, ps, u)
}

// checkFactor checks a second factor of a user: a WebAuthn answer to sd, a TOTP code, or a recovery code. It returns
// what was checked, for the audit log.
func (s *Server) checkFactor(ctx context.Context, u store.User, method, code string, wa json.RawMessage, sd *webauthn.SessionData) (string, error) {
	switch method {
	case store.FactorWebAuthn:
		if sd == nil {
			return "security key", fmt.Errorf("%w: no WebAuthn challenge was made", errFactor)
		}
		w, err := s.webauthnUser(ctx, u)
		if err != nil {
			return "", err
		}
		f, err := s.finishAssertion(ctx, w, *sd, wa)
		if err != nil {
			return "security key", err
		}
		return fmt.Sprintf("security key %q", f.Name), nil
	case store.FactorTOTP:
		fs, err := s.db.Factors(ctx, u.ID)
		if err != nil {
			return "", err
		}
		for _, f := range fs {
			if f.Kind != store.FactorTOTP {
				continue
			}
			secret, err := auth.ParseTOTPSecret(f.Data())
			if err != nil {
				return "", fmt.Errorf("factor %d: %w", f.ID, err)
			}
			step, ok := auth.VerifyTOTP(secret, code, s.now(), f.LastStep())
			if !ok {
				continue
			}
			if used, err := s.db.UseTOTP(ctx, f.ID, step); err != nil {
				return "", err
			} else if used {
				return fmt.Sprintf("authenticator app %q", f.Name), nil
			}
		}
		return "authenticator app code", fmt.Errorf("%w: wrong or already used code", errFactor)
	case "recovery_code":
		used, left, err := s.db.UseRecoveryCode(ctx, u.ID, auth.HashRecoveryCode(code))
		if err != nil {
			return "", err
		}
		if !used {
			return "recovery code", fmt.Errorf("%w: not a recovery code of this user, or used already", errFactor)
		}
		return fmt.Sprintf("recovery code (%d left)", left), nil
	}
	return "", fmt.Errorf("%w: method %q: webauthn, totp or recovery_code", store.ErrInvalid, method)
}

// afterFactor goes on after the second factor or an enrolment: a new password if one is needed, else the session.
func (s *Server) afterFactor(c *call, ps *pendingSignin, u store.User) (int, any, error) {
	ctx := c.r.Context()
	st, err := s.settings(ctx)
	if err != nil {
		return 0, nil, err
	}
	why := u.PasswordChange
	if exp := st.passwordExpiresAt(u); why == "" && !exp.IsZero() && !s.now().Before(exp) {
		why = "expired"
	}
	if why != "" {
		ps.stage, ps.why = stagePassword, why
		c.detail += "; a new password is needed (" + why + ")"
		return 200, SigninStep{Stage: stagePassword, SigninToken: s.signins.put(ps, s.now()), Username: u.Username, PasswordChange: why}, nil
	}
	return s.startSession(c, ps, u, st, "")
}

func (s *Server) loginPassword(c *call) (int, any, error) {
	var in NewPassword
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	ps, err := s.signins.get(in.SigninToken, s.now())
	if err != nil {
		return 0, nil, err
	}
	c.target = "user:" + ps.username
	if ps.stage != stagePassword {
		return 0, nil, fmt.Errorf("%w: this sign-in is at %s, not change_password", store.ErrInvalid, ps.stage)
	}
	if err := s.checkBreach(ctx, in.NewPassword); err != nil {
		return 0, nil, err
	}
	u, err := s.db.ChangeOwnPassword(ctx, ps.userID, in.NewPassword, 0)
	if err != nil {
		return 0, nil, err
	}
	s.signins.drop(in.SigninToken)
	c.detail = "new password set at sign-in (" + ps.why + "); other sessions ended"
	st, err := s.settings(ctx)
	if err != nil {
		return 0, nil, err
	}
	return s.startSession(c, ps, u, st, "")
}

// checkBreach asks the online breach check when it is on (§6). It cannot be made: the password is refused.
func (s *Server) checkBreach(ctx context.Context, p string) error {
	st, err := s.settings(ctx)
	if err != nil || !st.BreachCheck {
		return err
	}
	bad, err := auth.Breached(ctx, http.DefaultClient, st.BreachCheckURL, p)
	if err != nil {
		return fmt.Errorf("%w: the online breach check could not be made (%v): choose the password again later", store.ErrInvalid, err)
	}
	if bad {
		return fmt.Errorf("%w: %v: it appears in a known breach", store.ErrInvalid, auth.ErrWeakPassword)
	}
	return nil
}

// startSession ends a sign-in: the session, its cookie for a browser, and the notes a break-glass or new-device
// sign-in leaves (they notify admins). refresh is an OIDC session's refresh token.
func (s *Server) startSession(c *call, ps *pendingSignin, u store.User, st SigninSettings, refresh string) (int, any, error) {
	ctx := c.r.Context()
	tok, sess, err := s.db.CreateSession(ctx, store.NewSession{UserID: u.ID, Kind: ps.kind, Method: ps.method, BreakGlass: ps.breakGlass,
		RemoteAddr: ps.addr, UserAgent: ps.userAgent, DeviceID: ps.deviceID, Absolute: st.absolute(), IdPRefreshToken: refresh})
	if err != nil {
		return 0, nil, err
	}
	c.user, c.session = u, sess
	c.detail = fmt.Sprintf("%s; signed in (%s session %d, %s)", c.detail, ps.kind, sess.ID, ps.method)
	if ps.breakGlass {
		c.detail += "; break-glass while single sign-on is on"
		if err := s.db.NoteSignin(ctx, sess.ID, evBreakglass, fmt.Sprintf("break-glass sign-in of %s from %s while single sign-on is on", u.Username, ps.addr)); err != nil {
			return 0, nil, err
		}
	}
	if sess.NewDevice {
		c.detail += "; new device or address"
		if err := s.db.NoteSignin(ctx, sess.ID, evNewDevice, fmt.Sprintf("%s signed in from a device or address not seen in %d days: %s, %s",
			u.Username, int(store.DeviceMemory/(24*time.Hour)), ps.addr, orDash(ps.userAgent))); err != nil {
			return 0, nil, err
		}
	}
	out := SigninStep{Stage: stageDone, Username: u.Username, AbsoluteAt: sess.AbsoluteAt, IdleTimeoutS: int(st.idle() / time.Second),
		User: &u, NewDevice: sess.NewDevice, RecoveryCodes: ps.codes}
	if exp := st.passwordExpiresAt(u); !exp.IsZero() {
		out.PasswordExpiry = exp.UTC().Format(time.RFC3339)
	}
	if ps.kind == "browser" {
		http.SetCookie(c.w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", Expires: tsTime(sess.AbsoluteAt), HttpOnly: true, Secure: true,
			SameSite: http.SameSiteStrictMode})
	} else {
		out.Token = tok
	}
	return 200, out, nil
}

// tsTime reads a stored timestamp (zero when it is empty or unreadable).
func tsTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// deviceCookie is a browser's device id (§5: a sign-in from a device not seen in 30 days is reported). It is set at the
// first sign-in and lives 400 days, the longest a browser keeps a cookie.
const deviceCookieName = "__Host-gaia_device"

func (s *Server) deviceCookie(c *call) string {
	if ck, err := c.r.Cookie(deviceCookieName); err == nil && len(ck.Value) >= 32 {
		return ck.Value
	}
	v := auth.NewToken("d_")
	http.SetCookie(c.w, &http.Cookie{Name: deviceCookieName, Value: v, Path: "/", MaxAge: 400 * 24 * 3600, HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode})
	return v
}

// loginEnrol starts enrolling a factor during sign-in.
func (s *Server) loginEnrol(c *call) (int, any, error) {
	var in EnrolBegin
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ps, err := s.signins.get(in.SigninToken, s.now())
	if err != nil {
		return 0, nil, err
	}
	c.target = "user:" + ps.username
	if ps.stage != stageEnrol {
		return 0, nil, fmt.Errorf("%w: this sign-in is at %s, not enrol", store.ErrInvalid, ps.stage)
	}
	u, err := s.db.User(c.r.Context(), ps.userID)
	if err != nil {
		return 0, nil, err
	}
	ch, err := s.enrolBegin(c.r.Context(), u, in.Kind, ps)
	if err != nil {
		return 0, nil, err
	}
	c.detail = "enrolment of " + in.Kind + " started"
	return 200, ch, nil
}

// enrolBegin starts an enrolment for u; the ceremony is kept in ps.
func (s *Server) enrolBegin(ctx context.Context, u store.User, kind string, ps *pendingSignin) (EnrolChallenge, error) {
	if !contains(enrolKinds(u), kind) {
		return EnrolChallenge{}, fmt.Errorf("%w: %s may enrol %v, not %q", store.ErrInvalid, u.Username, enrolKinds(u), kind)
	}
	out := EnrolChallenge{Kind: kind}
	if kind == store.FactorTOTP {
		ps.totp = auth.NewTOTPSecret()
		out.TOTPSecret = auth.TOTPSecretText(ps.totp)
		out.TOTPURI = auth.TOTPURI(ps.totp, "Gaia "+s.instance, u.Username)
		return out, nil
	}
	w, err := s.webauthnUser(ctx, u)
	if err != nil {
		return EnrolChallenge{}, err
	}
	opts, sd, err := s.beginRegistration(w)
	if err != nil {
		return EnrolChallenge{}, err
	}
	ps.wa, out.WebAuthn = sd, opts
	return out, nil
}

// enrolFinish checks the authenticator's answer and stores the factor. keep is the session that stays (0: during
// sign-in, there is none).
func (s *Server) enrolFinish(ctx context.Context, actor string, u store.User, in EnrolFinish, ps *pendingSignin, keep int64) (store.Factor, error) {
	if !contains(enrolKinds(u), in.Kind) {
		return store.Factor{}, fmt.Errorf("%w: %s may enrol %v, not %q", store.ErrInvalid, u.Username, enrolKinds(u), in.Kind)
	}
	switch in.Kind {
	case store.FactorTOTP:
		if ps.totp == nil {
			return store.Factor{}, fmt.Errorf("%w: start the enrolment first", store.ErrInvalid)
		}
		step, ok := auth.VerifyTOTP(ps.totp, in.Code, s.now(), 0)
		if !ok {
			return store.Factor{}, fmt.Errorf("%w: the code does not match: check the app's clock and type the newest code", store.ErrInvalid)
		}
		f, err := s.db.AddFactor(ctx, actor, u.ID, store.FactorTOTP, factorName(in.Name, in.Kind), "", auth.TOTPSecretText(ps.totp), keep)
		if err != nil {
			return f, err
		}
		_, err = s.db.UseTOTP(ctx, f.ID, step) // the code that enrolled it is used
		return f, err
	default:
		if ps.wa == nil {
			return store.Factor{}, fmt.Errorf("%w: start the enrolment first", store.ErrInvalid)
		}
		w, err := s.webauthnUser(ctx, u)
		if err != nil {
			return store.Factor{}, err
		}
		id, data, err := s.finishRegistration(w, *ps.wa, in.WebAuthn)
		if err != nil {
			return store.Factor{}, err
		}
		return s.db.AddFactor(ctx, actor, u.ID, store.FactorWebAuthn, factorName(in.Name, in.Kind), id, data, keep)
	}
}

func (s *Server) loginEnrolFinish(c *call) (int, any, error) {
	var in EnrolFinish
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	ps, err := s.signins.get(in.SigninToken, s.now())
	if err != nil {
		return 0, nil, err
	}
	c.target = "user:" + ps.username
	if ps.stage != stageEnrol {
		return 0, nil, fmt.Errorf("%w: this sign-in is at %s, not enrol", store.ErrInvalid, ps.stage)
	}
	u, err := s.db.User(ctx, ps.userID)
	if err != nil {
		return 0, nil, err
	}
	f, err := s.enrolFinish(ctx, u.Username, u, in, ps, 0)
	if err != nil {
		return 0, nil, err
	}
	codes, err := s.newRecoveryCodes(ctx, u)
	if err != nil {
		return 0, nil, err
	}
	s.signins.drop(in.SigninToken)
	ps.method, ps.codes = f.Kind, codes
	c.detail = fmt.Sprintf("enrolled %s %q and %d recovery codes", f.Kind, f.Name, len(codes))
	if u, err = s.db.User(ctx, u.ID); err != nil {
		return 0, nil, err
	}
	return s.afterFactor(c, ps, u)
}

// newRecoveryCodes makes a user's 10 recovery codes (the old ones stop working) and returns them, to show once.
func (s *Server) newRecoveryCodes(ctx context.Context, u store.User) ([]string, error) {
	codes := auth.NewRecoveryCodes()
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.HashRecoveryCode(c)
	}
	return codes, s.db.SetRecoveryCodes(ctx, u.Username, u.ID, hashes)
}

// SigninOptions tell the sign-in page what to offer.
type SigninOptions struct {
	SSO      bool   `json:"sso"`
	SSOName  string `json:"sso_name,omitempty"`
	Instance string `json:"instance"`
}

func (s *Server) signinOptions(c *call) (int, any, error) {
	st, err := s.settings(c.r.Context())
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, SigninOptions{SSO: st.OIDC.Enabled, SSOName: st.OIDC.Name, Instance: s.instance}, nil
}

// instanceName is the installation's name in sign-in texts and the password rules.
func instanceName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "gaia"
	}
	return h
}
