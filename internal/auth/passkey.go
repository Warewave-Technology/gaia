package auth

import (
	"bytes"
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
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/store"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// flowTTL is how long a WebAuthn ceremony may take.
const flowTTL = 5 * time.Minute

// EnrollTTL is how long an enrollment link stays valid.
const EnrollTTL = 24 * time.Hour

// recoveryCodes is how many codes a person gets, shown once.
const recoveryCodes = 10

// webauthnUser adapts a person and their passkeys to the library.
type webauthnUser struct {
	id, name, display string
	creds             []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte                         { return []byte(u.id) }
func (u *webauthnUser) WebAuthnName() string                       { return u.name }
func (u *webauthnUser) WebAuthnDisplayName() string                { return u.display }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (a *Service) webauthnUser(ctx context.Context, id string) (*webauthnUser, error) {
	q := a.store.Q()
	u, err := q.User(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := q.Credentials(ctx, id)
	if err != nil {
		return nil, err
	}
	wu := &webauthnUser{id: u.ID, name: u.Username, display: u.DisplayName.String}
	if wu.display == "" {
		wu.display = u.Username
	}
	for _, row := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal(row.Credential, &c); err != nil {
			return nil, err
		}
		wu.creds = append(wu.creds, c)
	}
	return wu, nil
}

// relyingParty is Gaia as WebAuthn sees it: passkeys are bound to the host
// of AuthConfig.url, and they must verify the person (PIN, biometrics).
func (a *Service) relyingParty() (*webauthn.WebAuthn, error) {
	cfg, _, err := a.config()
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, err
	}
	return webauthn.New(&webauthn.Config{
		RPID: u.Hostname(), RPDisplayName: "Gaia", RPOrigins: []string{u.Scheme + "://" + u.Host},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
}

// saveFlow keeps a ceremony's server side until it is finished.
func (a *Service) saveFlow(ctx context.Context, kind, userID string, data any) (string, error) {
	return a.saveFlowTTL(ctx, kind, userID, data, flowTTL)
}

// saveFlowTTL is saveFlow with its own lifetime.
func (a *Service) saveFlowTTL(ctx context.Context, kind, userID string, data any, ttl time.Duration) (string, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	id := store.NewFlowID()
	return id, a.store.Q().InsertFlow(ctx, db.InsertFlowParams{
		ID: id, Kind: kind, UserID: text(userID), Data: b, ExpiresAt: pgtype.Timestamptz{Time: a.now().Add(ttl), Valid: true},
	})
}

// takeFlow returns a ceremony's data once.
func (a *Service) takeFlow(ctx context.Context, id, kind string, data any) (string, error) {
	f, err := a.store.Q().TakeFlow(ctx, db.TakeFlowParams{ID: id, Kind: kind})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("this sign-in step expired; start again")
	}
	if err != nil {
		return "", err
	}
	return f.UserID.String, json.Unmarshal(f.Data, data)
}

// CreateLocalUser records a local account and returns its enrollment link:
// the person opens it, registers a passkey and gets recovery codes. The
// token is in the URL's fragment, which browsers do not send to servers.
func (a *Service) CreateLocalUser(ctx context.Context, username, display string, teams []string, breakGlass bool) (string, error) {
	_, g, err := a.config()
	if err != nil {
		return "", err
	}
	if a.HasDirectory() {
		// With a directory, everyone is in it; local accounts are for
		// getting in when the directory or sign-in is broken (17.5).
		if !breakGlass {
			return "", errors.New("people sign in with the directory; a local account is a break-glass account only")
		}
		if !slices.Contains(g.RolesOf(teams), "platform-admin") {
			return "", errors.New("a break-glass account is a platform admin's: give it a team the Policy makes platform-admin")
		}
	}
	if !validUsername(username) {
		return "", fmt.Errorf("username %q: lowercase letters, digits, '.', '-' and '_'", username)
	}
	id := store.NewUserID()
	err = a.store.Tx(ctx, func(q *db.Queries) error {
		if err := q.InsertUser(ctx, db.InsertUserParams{ID: id, Username: username, DisplayName: text(display), Provider: "local"}); err != nil {
			return err
		}
		for _, t := range teams {
			if err := q.AddTeam(ctx, db.AddTeamParams{UserID: id, Team: t, Source: "local"}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return a.Enroll(ctx, id)
}

// Enroll returns a new enrollment link for an existing local account, for
// a person who lost every passkey and recovery code.
func (a *Service) Enroll(ctx context.Context, userID string) (string, error) {
	cfg, _, err := a.config()
	if err != nil {
		return "", err
	}
	token, _, _, err := a.newSession(ctx, session{userID: userID, kind: KindEnroll, ttl: EnrollTTL})
	if err != nil {
		return "", err
	}
	return strings.TrimRight(cfg.URL, "/") + "/enroll#" + token, nil
}

// enrollment returns the person an enrollment token was issued for.
func (a *Service) enrollment(ctx context.Context, token string) (db.SessionByTokenRow, error) {
	s, err := a.store.Q().SessionByToken(ctx, hashToken(token))
	if err != nil || s.Kind != KindEnroll || s.RevokedAt.Valid || !a.now().Before(s.ExpiresAt.Time) || !s.Enabled {
		return s, errors.New("this enrollment link is not valid any more; ask a platform admin for a new one")
	}
	return s, nil
}

type flowStart struct {
	Flow    string `json:"flow"`
	Options any    `json:"options"`
}

// BeginEnroll starts registering a passkey with an enrollment token.
func (a *Service) BeginEnroll(ctx context.Context, token string) (*flowStart, error) {
	s, err := a.enrollment(ctx, token)
	if err != nil {
		return nil, err
	}
	return a.beginRegister(ctx, s.UserID)
}

func (a *Service) beginRegister(ctx context.Context, userID string) (*flowStart, error) {
	rp, err := a.relyingParty()
	if err != nil {
		return nil, err
	}
	u, err := a.webauthnUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range u.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, sd, err := rp.BeginRegistration(u, webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, err
	}
	id, err := a.saveFlow(ctx, "webauthn-register", userID, sd)
	if err != nil {
		return nil, err
	}
	return &flowStart{Flow: id, Options: opts}, nil
}

// finishRegister checks the authenticator's answer and keeps the passkey.
func (a *Service) finishRegister(ctx context.Context, flow, name string, response json.RawMessage) (string, error) {
	var sd webauthn.SessionData
	userID, err := a.takeFlow(ctx, flow, "webauthn-register", &sd)
	if err != nil {
		return "", err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(response))
	if err != nil {
		return "", fmt.Errorf("passkey: %w", err)
	}
	rp, err := a.relyingParty()
	if err != nil {
		return "", err
	}
	u, err := a.webauthnUser(ctx, userID)
	if err != nil {
		return "", err
	}
	cred, err := rp.CreateCredential(u, sd, parsed)
	if err != nil {
		return "", fmt.Errorf("passkey: %w", err)
	}
	b, err := json.Marshal(cred)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = "passkey " + a.now().Format("2006-01-02")
	}
	return userID, a.store.Q().InsertCredential(ctx, db.InsertCredentialParams{
		ID: cred.ID, UserID: userID, Name: trunc(name, 80), Credential: b, SignCount: int64(cred.Authenticator.SignCount),
	})
}

// FinishEnroll keeps the passkey, ends the enrollment link, gives the
// person new recovery codes and signs them in.
func (a *Service) FinishEnroll(ctx context.Context, w http.ResponseWriter, r *http.Request, token, flow, name string, response json.RawMessage) ([]string, error) {
	s, err := a.enrollment(ctx, token)
	if err != nil {
		return nil, err
	}
	userID, err := a.finishRegister(ctx, flow, name, response)
	if err != nil {
		return nil, err
	}
	if userID != s.UserID {
		return nil, errors.New("this sign-in step belongs to another enrollment")
	}
	if _, err := a.store.Q().RevokeSession(ctx, db.RevokeSessionParams{ID: s.ID, UserID: s.UserID}); err != nil {
		return nil, err
	}
	codes, err := a.newRecoveryCodes(ctx, userID)
	if err != nil {
		return nil, err
	}
	return codes, a.uiSession(ctx, w, r, userID, true)
}

// BeginPasskeyLogin starts a sign-in with a passkey; the browser offers
// the person's passkeys for this site, so no username is asked.
func (a *Service) BeginPasskeyLogin(ctx context.Context) (*flowStart, error) {
	rp, err := a.relyingParty()
	if err != nil {
		return nil, err
	}
	opts, sd, err := rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, err
	}
	id, err := a.saveFlow(ctx, "webauthn-login", "", sd)
	if err != nil {
		return nil, err
	}
	return &flowStart{Flow: id, Options: opts}, nil
}

// FinishPasskeyLogin checks the assertion and starts a browser session.
func (a *Service) FinishPasskeyLogin(ctx context.Context, w http.ResponseWriter, r *http.Request, flow string, response json.RawMessage) (*Principal, error) {
	var sd webauthn.SessionData
	if _, err := a.takeFlow(ctx, flow, "webauthn-login", &sd); err != nil {
		return nil, err
	}
	userID, err := a.assert(ctx, sd, "", response)
	if err != nil {
		return nil, err
	}
	if err := a.uiSession(ctx, w, r, userID, true); err != nil {
		return nil, err
	}
	u, err := a.store.Q().User(ctx, userID)
	if err != nil {
		return nil, err
	}
	return a.principal(ctx, u.ID, u.Username, "", KindUI, pgtype.Timestamptz{Time: a.now(), Valid: true})
}

var errPasskey = errors.New("the passkey was not accepted")

// assert validates an assertion; userID is "" for a discoverable login.
// It returns the person and updates the passkey's counter, refusing a
// counter that went back (a cloned authenticator).
func (a *Service) assert(ctx context.Context, sd webauthn.SessionData, userID string, response json.RawMessage) (string, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(response))
	if err != nil {
		return "", errPasskey
	}
	rp, err := a.relyingParty()
	if err != nil {
		return "", err
	}
	var u *webauthnUser
	var cred *webauthn.Credential
	if userID == "" {
		_, cred, err = rp.ValidatePasskeyLogin(func(_, handle []byte) (webauthn.User, error) {
			found, err := a.webauthnUser(ctx, string(handle))
			if err != nil {
				return nil, err
			}
			u = found
			return found, nil
		}, sd, parsed)
	} else {
		if u, err = a.webauthnUser(ctx, userID); err == nil {
			cred, err = rp.ValidateLogin(u, sd, parsed)
		}
	}
	if err != nil {
		a.log.Info("passkey refused", "err", err)
		return "", errPasskey
	}
	if cred.Authenticator.CloneWarning {
		return "", errors.New("this passkey's counter went back; it may be cloned and is refused")
	}
	full, err := a.store.Q().User(ctx, u.id)
	if err != nil || !full.Enabled {
		return "", errPasskey
	}
	b, err := json.Marshal(cred)
	if err != nil {
		return "", err
	}
	if err := a.store.Q().UseCredential(ctx, db.UseCredentialParams{ID: cred.ID, Credential: b, SignCount: int64(cred.Authenticator.SignCount)}); err != nil {
		return "", err
	}
	return u.id, nil
}

// newRecoveryCodes replaces a person's recovery codes and returns the new
// ones; only their hashes are kept.
func (a *Service) newRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes := make([]string, recoveryCodes)
	err := a.store.Tx(ctx, func(q *db.Queries) error {
		if err := q.DeleteRecoveryCodes(ctx, userID); err != nil {
			return err
		}
		for i := range codes {
			codes[i] = recoveryCode()
			if err := q.InsertRecoveryCode(ctx, db.InsertRecoveryCodeParams{UserID: userID, CodeHash: hashToken(normalizeCode(codes[i]))}); err != nil {
				return err
			}
		}
		return nil
	})
	return codes, err
}

// recoveryCode is 10 characters of an alphabet without look-alikes, shown
// as two groups of five.
func recoveryCode() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:5]) + "-" + string(b[5:])
}

func normalizeCode(c string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(c), "-", ""))
}

// RecoveryLogin signs a local account in with one of its recovery codes,
// which is then used up. The person should register a new passkey.
func (a *Service) RecoveryLogin(ctx context.Context, w http.ResponseWriter, r *http.Request, username, code string) error {
	refused := errors.New("the username or the recovery code is wrong")
	u, err := a.store.Q().UserByName(ctx, username)
	if err != nil || u.Provider != "local" || !u.Enabled {
		return refused
	}
	n, err := a.store.Q().UseRecoveryCode(ctx, db.UseRecoveryCodeParams{UserID: u.ID, CodeHash: hashToken(normalizeCode(code))})
	if err != nil {
		return err
	}
	if n != 1 {
		return refused
	}
	return a.uiSession(ctx, w, r, u.ID, true)
}

func validUsername(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

func encodeID(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func decodeID(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
