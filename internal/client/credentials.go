package client

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zalando/go-keyring"
)

// Credentials are a CLI session with one server (design 11.3).
type Credentials struct {
	Server           string    `json:"server"`
	Username         string    `json:"username,omitempty"`
	AccessToken      string    `json:"accessToken"`
	AccessExpiresAt  time.Time `json:"accessExpiresAt"`
	RefreshToken     string    `json:"refreshToken"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
}

// keyringService is the OS keychain entry's service; the account is the
// server's address.
const keyringService = "gaia"

// ErrNoCredentials: not signed in to this server.
var ErrNoCredentials = errors.New("not signed in; run gaia login")

// Save keeps the credentials in the OS keychain, or, where there is none
// (a headless Linux), in a file only the user can read.
func Save(c Credentials) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := keyring.Set(keyringService, c.Server, string(b)); err == nil {
		return nil
	}
	path, err := credentialsFile(c.Server)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Load returns the saved credentials for server.
func Load(server string) (Credentials, error) {
	var c Credentials
	s, err := keyring.Get(keyringService, server)
	if err != nil {
		path, perr := credentialsFile(server)
		if perr != nil {
			return c, perr
		}
		b, ferr := os.ReadFile(path) //nolint:gosec // our own file
		if ferr != nil {
			return c, ErrNoCredentials
		}
		s = string(b)
	}
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return c, ErrNoCredentials
	}
	return c, nil
}

// Forget removes the saved credentials for server.
func Forget(server string) {
	_ = keyring.Delete(keyringService, server)
	if path, err := credentialsFile(server); err == nil {
		_ = os.Remove(path)
	}
}

func credentialsFile(server string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gaia", "credentials", sanitize(server)+".json"), nil
}

func sanitize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
