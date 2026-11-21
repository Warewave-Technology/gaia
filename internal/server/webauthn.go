package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// WebAuthn (ADR-008 §3, §4): Gaia is the relying party named by public_url. Any WebAuthn authenticator is accepted, a
// security key or the platform's own (Touch ID, Windows Hello), with attestation "none": Gaia does not need to know
// the make, only that the same key answers.

// resolvePublicURL checks public_url, or makes it from the host name and the listen port.
func (c *Config) resolvePublicURL() error {
	if c.PublicURL == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "localhost"
		}
		_, port, _ := net.SplitHostPort(c.Listen)
		if port == "" {
			port = "8443"
		}
		c.PublicURL = "https://" + net.JoinHostPort(h, port)
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return fmt.Errorf("public_url %q: want https://NAME[:PORT]", c.PublicURL)
	}
	c.PublicURL = "https://" + u.Host
	return nil
}

// rpID is public_url's host name: WebAuthn's relying party ID.
func (c Config) rpID() string {
	u, _ := url.Parse(c.PublicURL) // checked by resolve
	return u.Hostname()
}

func newWebAuthn(cfg Config, instance string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:          cfg.rpID(),
		RPDisplayName: "Gaia " + instance,
		RPOrigins:     []string{cfg.PublicURL},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementDiscouraged,
			UserVerification: protocol.VerificationPreferred,
		},
		AttestationPreference: protocol.PreferNoAttestation,
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: 2 * time.Minute, TimeoutUVD: 2 * time.Minute},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: 2 * time.Minute, TimeoutUVD: 2 * time.Minute},
		},
	})
}

// waUser is a user with its WebAuthn keys, as go-webauthn wants it.
type waUser struct {
	u       store.User
	factors []store.Factor // the WebAuthn ones, in the order of creds
	creds   []webauthn.Credential
}

func (w *waUser) WebAuthnID() []byte                         { return []byte(fmt.Sprintf("gaia-user-%d", w.u.ID)) }
func (w *waUser) WebAuthnName() string                       { return w.u.Username }
func (w *waUser) WebAuthnDisplayName() string                { return w.u.Username }
func (w *waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func (s *Server) webauthnUser(ctx context.Context, u store.User) (*waUser, error) {
	fs, err := s.db.Factors(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	w := &waUser{u: u}
	for _, f := range fs {
		if f.Kind != store.FactorWebAuthn {
			continue
		}
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(f.Data()), &c); err != nil {
			return nil, fmt.Errorf("factor %d of %s: %w", f.ID, u.Username, err)
		}
		w.factors = append(w.factors, f)
		w.creds = append(w.creds, c)
	}
	return w, nil
}

// errFactor is a second factor that did not pass: a failed sign-in step (ADR-008 §7).
var errFactor = errors.New("the second factor did not pass")

// beginAssertion starts a WebAuthn assertion for a user's keys; challenge nil makes a random one (a sign-in), a step-up
// passes one bound to its action.
func (s *Server) beginAssertion(w *waUser, challenge []byte) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	if len(w.creds) == 0 {
		return nil, nil, fmt.Errorf("%w: %s has no security key", store.ErrInvalid, w.u.Username)
	}
	var opts []webauthn.LoginOption
	if challenge != nil {
		opts = append(opts, webauthn.WithChallenge(challenge))
	}
	return s.webauthn.BeginLogin(w, opts...)
}

// finishAssertion checks a browser's answer to an assertion and records the key's use. It returns the key's factor.
func (s *Server) finishAssertion(ctx context.Context, w *waUser, sd webauthn.SessionData, raw json.RawMessage) (store.Factor, error) {
	if len(raw) == 0 {
		return store.Factor{}, fmt.Errorf("%w: no WebAuthn answer", errFactor)
	}
	p, err := protocol.ParseCredentialRequestResponseBytes(raw)
	if err != nil {
		return store.Factor{}, fmt.Errorf("%w: the WebAuthn answer: %v", errFactor, err)
	}
	c, err := s.webauthn.ValidateLogin(w, sd, p)
	if err != nil {
		return store.Factor{}, fmt.Errorf("%w: %v", errFactor, err)
	}
	if c.Authenticator.CloneWarning {
		return store.Factor{}, fmt.Errorf("%w: the key's signature counter went back: it may have been copied", errFactor)
	}
	for i, k := range w.creds {
		if !bytes.Equal(k.ID, c.ID) {
			continue
		}
		b, err := json.Marshal(c)
		if err != nil {
			return store.Factor{}, err
		}
		if err := s.db.UpdateWebAuthn(ctx, w.factors[i].ID, string(b)); err != nil {
			return store.Factor{}, err
		}
		return w.factors[i], nil
	}
	return store.Factor{}, fmt.Errorf("%w: an unknown key", errFactor)
}

// beginRegistration starts enrolling a new key; the user's other keys are excluded (one registration per key).
func (s *Server) beginRegistration(w *waUser) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	var excl []protocol.CredentialDescriptor
	for _, c := range w.creds {
		excl = append(excl, c.Descriptor())
	}
	return s.webauthn.BeginRegistration(w, webauthn.WithExclusions(excl), webauthn.WithConveyancePreference(protocol.PreferNoAttestation))
}

// finishRegistration checks a browser's answer to a registration and returns the new credential as stored.
func (s *Server) finishRegistration(w *waUser, sd webauthn.SessionData, raw json.RawMessage) (credID, data string, err error) {
	if len(raw) == 0 {
		return "", "", fmt.Errorf("%w: no WebAuthn answer", store.ErrInvalid)
	}
	p, err := protocol.ParseCredentialCreationResponseBytes(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: the WebAuthn answer: %v", store.ErrInvalid, err)
	}
	c, err := s.webauthn.CreateCredential(w, sd, p)
	if err != nil {
		return "", "", fmt.Errorf("%w: the key was not enrolled: %v", store.ErrInvalid, err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(c.ID), string(b), nil
}

// factorName is a factor's name as typed, or a default.
func factorName(name, kind string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	if kind == store.FactorTOTP {
		return "authenticator app"
	}
	return "security key"
}
