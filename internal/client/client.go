// Package client is the CLI's client of the gaia server API (ADR-007 §6: the CLI uses the API). The server's TLS
// certificate is pinned by its sha256, confirmed by the operator at the first login.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the CLI's saved login (mode 0600).
type Config struct {
	Server     string `json:"server"`      // e.g. https://gaia.lab:8443
	CertSHA256 string `json:"cert_sha256"` // the pinned server certificate
	Username   string `json:"username"`
	Token      string `json:"token"`
	ExpiresAt  string `json:"expires_at"`
}

// ConfigPath is $GAIA_CLI_CONFIG, or gaia/cli.json in the user's config directory.
func ConfigPath() (string, error) {
	if p := os.Getenv("GAIA_CLI_CONFIG"); p != "" {
		return p, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "gaia", "cli.json"), nil
}

// Load reads the saved login.
func Load() (Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, errors.New("not logged in: run gaia login -server https://HOST:8443 -user NAME")
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}

// Save writes the login with mode 0600.
func (c Config) Save() error {
	p, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Fingerprint is the colon-separated upper-case hex sha256 of a certificate (as gaia server init prints it).
func Fingerprint(der []byte) string {
	h := sha256.Sum256(der)
	s := strings.ToUpper(hex.EncodeToString(h[:]))
	var parts []string
	for i := 0; i < len(s); i += 2 {
		parts = append(parts, s[i:i+2])
	}
	return strings.Join(parts, ":")
}

func normalise(fp string) string {
	return strings.ToUpper(strings.NewReplacer(":", "", " ", "").Replace(fp))
}

// ServerFingerprint connects to an https server without trusting it and returns its certificate's fingerprint.
func ServerFingerprint(ctx context.Context, server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("server %q: want https://HOST:PORT", server)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "443")
	}
	d := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} // only to read the certificate
	conn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("the server sent no certificate")
	}
	return Fingerprint(certs[0].Raw), nil
}

// Client calls the API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New returns a client that only talks to the server whose certificate has the pinned fingerprint.
func New(c Config) *Client {
	pin := normalise(c.CertSHA256)
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12,
		// The certificate is self-signed: trust is the pinned fingerprint, checked here instead of a CA chain.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || normalise(Fingerprint(cs.PeerCertificates[0].Raw)) != pin {
				return errors.New("the server's certificate is not the one pinned at login (log in again only if it was replaced on purpose)")
			}
			return nil
		}}}
	return &Client{base: strings.TrimSuffix(c.Server, "/"), token: c.Token, http: &http.Client{Transport: tr, Timeout: 10 * time.Minute}}
}

// APIError is an error answer of the server.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"error"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// Do sends a request (in: nil, raw JSON bytes, or a value to encode) and returns the response body.
func (c *Client) Do(ctx context.Context, method, path string, in any) ([]byte, http.Header, error) {
	var body io.Reader
	switch v := in.(type) {
	case nil:
	case []byte:
		body = bytes.NewReader(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		e := &APIError{Status: resp.StatusCode}
		if json.Unmarshal(b, e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		return b, resp.Header, e
	}
	return b, resp.Header, nil
}
