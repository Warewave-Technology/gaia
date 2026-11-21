package server

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Sign-in and security settings (ADR-008): what the ADR leaves to choose. Everything else in the ADR is fixed and
// has no setting (store/sessions.go, store/tokens.go, auth/password.go, stepup.go). Every change needs a step-up, is a
// state change (entity setting) and an audit entry, and is listed in the next daily report.

const settingSignin = "signin"

// SigninSettings are the choices.
type SigninSettings struct {
	OIDC             OIDCSettings `json:"oidc"`
	IdleTimeoutMin   int          `json:"idle_timeout_min"`   // 5-60, default 15 (§5)
	AbsoluteTimeoutH int          `json:"absolute_timeout_h"` // 1-12, default 8 (§5)
	PasswordMaxAge   string       `json:"password_max_age"`   // none (default), 90d or 180d (§6)
	BreachCheck      bool         `json:"online_breach_check"`
	BreachCheckURL   string       `json:"breach_check_url"` // the k-anonymity range API (default Have I Been Pwned's)
}

// OIDCSettings configure single sign-on (§1, §2, §3). The client secret is not here: it is a file in secrets/.
type OIDCSettings struct {
	Enabled     bool              `json:"enabled"`
	Name        string            `json:"name"` // the button says "Continue with <name>"
	Issuer      string            `json:"issuer"`
	ClientID    string            `json:"client_id"`
	GroupsClaim string            `json:"groups_claim"` // default groups
	Roles       map[string]string `json:"roles"`        // IdP group -> admin, operator or auditor
	// The ID token must show a second factor: claim (acr or amr) holding one of values. Required by default.
	SecondFactor SecondFactorRule `json:"second_factor"`
	CAPEM        string           `json:"ca_pem,omitempty"` // the CA of the IdP's TLS certificate when the system's do not sign it
}

// SecondFactorRule is how an ID token shows a second factor.
type SecondFactorRule struct {
	Required bool     `json:"required"`
	Claim    string   `json:"claim"`  // acr or amr
	Values   []string `json:"values"` // any one of them
}

func defaultSettings() SigninSettings {
	return SigninSettings{
		IdleTimeoutMin: 15, AbsoluteTimeoutH: 8, PasswordMaxAge: "none", BreachCheckURL: auth.DefaultBreachURL,
		OIDC: OIDCSettings{Name: "single sign-on", GroupsClaim: "groups", Roles: map[string]string{},
			SecondFactor: SecondFactorRule{Required: true, Claim: "acr", Values: []string{"mfa"}}},
	}
}

// settings returns the settings in force.
func (s *Server) settings(ctx context.Context) (SigninSettings, error) {
	v := defaultSettings()
	if _, err := s.db.Setting(ctx, settingSignin, &v); err != nil {
		return v, err
	}
	if v.OIDC.Roles == nil {
		v.OIDC.Roles = map[string]string{}
	}
	return v, nil
}

func (v SigninSettings) idle() time.Duration { return time.Duration(v.IdleTimeoutMin) * time.Minute }
func (v SigninSettings) absolute() time.Duration {
	return time.Duration(v.AbsoluteTimeoutH) * time.Hour
}

// passwordMaxAge is the age at which a password must be changed (0 = never).
func (v SigninSettings) passwordMaxAge() time.Duration {
	switch v.PasswordMaxAge {
	case "90d":
		return 90 * 24 * time.Hour
	case "180d":
		return 180 * 24 * time.Hour
	}
	return 0
}

// passwordExpiresAt is when a local user's password must be changed (zero: never).
func (v SigninSettings) passwordExpiresAt(u store.User) time.Time {
	age := v.passwordMaxAge()
	if age == 0 || u.Source != store.SourceLocal {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, u.PasswordChangedAt)
	if err != nil {
		return time.Unix(0, 0) // unreadable: expired
	}
	return t.Add(age)
}

// PasswordReminder is how long before its expiry a user is told to change the password (§6).
const PasswordReminder = 14 * 24 * time.Hour

func (v SigninSettings) check() error {
	switch {
	case v.IdleTimeoutMin < 5 || v.IdleTimeoutMin > 60:
		return fmt.Errorf("%w: idle_timeout_min %d: 5 to 60", store.ErrInvalid, v.IdleTimeoutMin)
	case v.AbsoluteTimeoutH < 1 || v.AbsoluteTimeoutH > 12:
		return fmt.Errorf("%w: absolute_timeout_h %d: 1 to 12", store.ErrInvalid, v.AbsoluteTimeoutH)
	case !slices.Contains([]string{"none", "90d", "180d"}, v.PasswordMaxAge):
		return fmt.Errorf("%w: password_max_age %q: none, 90d or 180d", store.ErrInvalid, v.PasswordMaxAge)
	}
	if v.BreachCheck {
		if u, err := url.Parse(v.BreachCheckURL); err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("%w: breach_check_url %q: an https URL", store.ErrInvalid, v.BreachCheckURL)
		}
	}
	o := v.OIDC
	if !o.Enabled {
		return nil
	}
	if u, err := url.Parse(o.Issuer); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: oidc.issuer %q: an https URL", store.ErrInvalid, o.Issuer)
	}
	if o.ClientID == "" || o.GroupsClaim == "" || strings.TrimSpace(o.Name) == "" {
		return fmt.Errorf("%w: oidc needs client_id, groups_claim and name", store.ErrInvalid)
	}
	if len(o.Roles) == 0 {
		return fmt.Errorf("%w: oidc.roles: map at least one group to a role (no matching group, no access)", store.ErrInvalid)
	}
	for g, r := range o.Roles {
		if g == "" || !store.ValidRole(r) {
			return fmt.Errorf("%w: oidc.roles %q -> %q: a group and admin, operator or auditor", store.ErrInvalid, g, r)
		}
	}
	sf := o.SecondFactor
	if sf.Required && ((sf.Claim != "acr" && sf.Claim != "amr") || len(sf.Values) == 0) {
		return fmt.Errorf("%w: oidc.second_factor: claim acr or amr, and the values that show a second factor", store.ErrInvalid)
	}
	return nil
}

// oidcSecretFile holds the OIDC client secret (0600, never in an answer or the catalog).
func (c Config) oidcSecretFile() string { return filepath.Join(c.secretsDir(), "oidc-client-secret") }

func (s *Server) oidcSecret() (string, error) {
	b, err := os.ReadFile(s.cfg.oidcSecretFile())
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: the OIDC client secret is not set", store.ErrInvalid)
	}
	return strings.TrimSpace(string(b)), err
}

// SettingsView is the settings with what cannot be chosen (ADR-008's fixed rules, so the screen can list them) and the
// state of the IdP.
type SettingsView struct {
	SigninSettings
	OIDCSecretSet bool        `json:"oidc_client_secret_set"`
	OIDCStatus    OIDCStatus  `json:"oidc_status"`
	PublicURL     string      `json:"public_url"`
	Fixed         FixedRules  `json:"fixed"`
	BreakGlass    []string    `json:"break_glass_admins"`
	StepUpActions []string    `json:"step_up_actions"`
	Blocklist     BlocklistID `json:"password_blocklist"`
}

// BlocklistID names the embedded password blocklist.
type BlocklistID struct {
	Version string `json:"version"`
	Entries int    `json:"entries"`
}

// FixedRules are ADR-008's rules that have no setting.
type FixedRules struct {
	SecondFactorRequired bool   `json:"second_factor_required"`
	AdminFactor          string `json:"admin_factor"`
	PasswordMinLength    int    `json:"password_min_length"`
	PasswordMaxLength    int    `json:"password_max_length"`
	PasswordHistory      int    `json:"password_history"`
	SlowDownFrom         int    `json:"slow_down_from_failure"`
	PauseAfter           int    `json:"pause_after_failures"`
	PauseWindowMin       int    `json:"pause_window_min"`
	PauseMin             int    `json:"pause_min"`
	AuditorMaxDays       int    `json:"auditor_max_days"`
	SessionsPerUser      int    `json:"sessions_per_user"`
	StepUpSeconds        int    `json:"step_up_s"`
	OIDCRefreshMin       int    `json:"oidc_refresh_min"`
	NewDeviceDays        int    `json:"new_device_days"`
	TokenMaxDays         int    `json:"api_token_max_days"`
	Cookie               string `json:"cookie"`
}

var fixedRules = FixedRules{
	SecondFactorRequired: true, AdminFactor: "webauthn", PasswordMinLength: auth.MinPasswordLength, PasswordMaxLength: auth.MaxPasswordLength,
	PasswordHistory: auth.PasswordHistory, SlowDownFrom: store.FailuresBeforeWait + 1, PauseAfter: store.FailuresToPause,
	PauseWindowMin: int(store.FailureWindow / time.Minute), PauseMin: int(store.PauseFor / time.Minute), AuditorMaxDays: int(store.AuditorMaxAge / (24 * time.Hour)),
	SessionsPerUser: store.MaxSessionsPerUser, StepUpSeconds: int(stepUpWindow / time.Second), OIDCRefreshMin: int(oidcRefreshEvery / time.Minute),
	NewDeviceDays: int(store.DeviceMemory / (24 * time.Hour)), TokenMaxDays: int(store.TokenMaxAge / (24 * time.Hour)),
	Cookie: sessionCookie + "; Secure; HttpOnly; SameSite=Strict; Path=/",
}

func (s *Server) settingsView(c *call) (int, any, error) {
	ctx := c.r.Context()
	v, err := s.settings(ctx)
	if err != nil {
		return 0, nil, err
	}
	out := SettingsView{SigninSettings: v, OIDCStatus: s.oidc.status(), PublicURL: s.cfg.PublicURL, Fixed: fixedRules,
		StepUpActions: stepUpActions(s.routes), BreakGlass: []string{}}
	_, err = os.Stat(s.cfg.oidcSecretFile())
	out.OIDCSecretSet = err == nil
	out.Blocklist.Version = auth.BlocklistVersion
	if out.Blocklist.Entries, err = auth.BlocklistSize(); err != nil {
		return 0, nil, err
	}
	us, err := s.db.Users(ctx)
	if err != nil {
		return 0, nil, err
	}
	for _, u := range us {
		if u.BreakGlass && !u.Disabled && u.Role == store.RoleAdmin {
			out.BreakGlass = append(out.BreakGlass, u.Username)
		}
	}
	return 200, out, nil
}

// SettingsUpdate replaces the settings. oidc_client_secret, when set, replaces the secret (it is never shown again).
type SettingsUpdate struct {
	SigninSettings
	OIDCClientSecret string `json:"oidc_client_secret,omitempty"`
}

func (s *Server) putSettings(c *call) (int, any, error) {
	var in SettingsUpdate
	if err := c.decode(&in); err != nil {
		return 0, nil, err
	}
	ctx := c.r.Context()
	v := in.SigninSettings
	if v.BreachCheckURL == "" {
		v.BreachCheckURL = auth.DefaultBreachURL
	}
	if v.OIDC.Roles == nil {
		v.OIDC.Roles = map[string]string{}
	}
	if err := v.check(); err != nil {
		return 0, nil, err
	}
	if in.OIDCClientSecret != "" {
		if err := writeSecret(s.cfg.oidcSecretFile(), in.OIDCClientSecret); err != nil {
			return 0, nil, err
		}
		c.detail = "OIDC client secret replaced; "
	}
	if v.OIDC.Enabled {
		if _, err := s.oidcSecret(); err != nil {
			return 0, nil, err
		}
		// OIDC on needs a way in when the IdP is down (§1).
		us, err := s.db.Users(ctx)
		if err != nil {
			return 0, nil, err
		}
		ok := false
		for _, u := range us {
			ok = ok || (u.BreakGlass && !u.Disabled && u.Role == store.RoleAdmin && len(u.Factors) > 0)
		}
		if !ok {
			return 0, nil, fmt.Errorf("%w: OIDC needs a break-glass admin with an enrolled security key first", store.ErrConflict)
		}
		if err := s.oidc.check(ctx, v.OIDC); err != nil {
			return 0, nil, fmt.Errorf("%w: the identity provider: %v", store.ErrInvalid, err)
		}
	}
	if err := s.db.SetSetting(ctx, c.user.Username, settingSignin, v); err != nil {
		return 0, nil, err
	}
	s.oidc.reset()
	c.detail += "sign-in settings changed"
	return 200, v, nil
}

// writeSecret replaces a secret file (0600) atomically.
func writeSecret(path, value string) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(value+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
