package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// loginTimeout is how long gaia login waits for the browser.
const loginTimeout = 5 * time.Minute

// Login signs the CLI in through the browser (RFC 8252): it listens on a
// loopback port, opens the server's approval page, and trades the code it
// gets back, with its PKCE verifier, for tokens.
func (c *Client) Login(ctx context.Context, open func(string) error, out io.Writer) (*Credentials, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = ln.Close() }()
	redirect := fmt.Sprintf("http://%s/callback", ln.Addr().String())
	verifier, state := randomString(32), randomString(16)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	type result struct{ code string }
	got := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state || q.Get("code") == "" {
			http.Error(w, "this sign-in does not belong to this gaia login", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><title>Gaia</title><p style=\"font-family:system-ui;margin:20vh auto;max-width:28em\">Signed in. You can close this tab and go back to the terminal.</p>")
		select {
		case got <- result{code: q.Get("code")}:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	u := c.Base + "/cli-login?" + url.Values{"redirect_uri": {redirect}, "state": {state}, "code_challenge": {challenge}}.Encode()
	_, _ = fmt.Fprintf(out, "Opening the browser to sign in. If it does not open, visit:\n  %s\n", u)
	if open != nil {
		_ = open(u)
	}
	wait, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	var res result
	select {
	case res = <-got:
	case <-wait.Done():
		return nil, errors.New("the browser did not finish the sign-in in time")
	}
	var t Credentials
	if err := c.post(ctx, "/api/v1/auth/cli/token", map[string]string{"code": res.code, "codeVerifier": verifier, "redirectUri": redirect}, &t); err != nil {
		return nil, err
	}
	t.Server = c.Base
	return &t, nil
}

// DeviceLogin signs in where no browser runs (RFC 8628): the person opens
// the address on another device, checks where the request comes from and
// approves the code.
func (c *Client) DeviceLogin(ctx context.Context, out io.Writer) (*Credentials, error) {
	var d struct {
		DeviceCode      string `json:"deviceCode"`
		UserCode        string `json:"userCode"`
		VerificationURI string `json:"verificationUri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expiresIn"`
	}
	if err := c.post(ctx, "/api/v1/auth/device", map[string]string{}, &d); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(out, "Open %s in a browser where you are signed in to Gaia and enter the code:\n\n    %s\n\n", d.VerificationURI, d.UserCode)
	interval := time.Duration(d.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		var t Credentials
		err := c.post(ctx, "/api/v1/auth/device/token", map[string]string{"deviceCode": d.DeviceCode}, &t)
		if err == nil {
			t.Server = c.Base
			return &t, nil
		}
		var e *Error
		if !errors.As(err, &e) {
			return nil, err
		}
		switch e.Message {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return nil, errors.New("the sign-in was denied")
		default:
			return nil, fmt.Errorf("device sign-in: %s", e.Message)
		}
	}
	return nil, errors.New("the code expired")
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
