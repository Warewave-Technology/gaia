// Package client talks to gaia server's API (api/openapi.yaml).
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/server"
)

// Client is an API client.
type Client struct {
	Base string // http://127.0.0.1:8080
	HTTP *http.Client
	// creds sign requests in when set; they are refreshed and saved as
	// their access token runs out.
	creds *Credentials
	save  func(Credentials) error
}

// WithCredentials signs every request in with c, refreshing its access
// token through save when it runs out.
func (c *Client) WithCredentials(cr Credentials, save func(Credentials) error) *Client {
	c.creds, c.save = &cr, save
	return c
}

// refreshEarly is how long before its end an access token is renewed.
const refreshEarly = time.Minute

func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	req.Header.Set("X-Gaia-Request", "1")
	if c.creds == nil {
		return nil
	}
	if time.Until(c.creds.AccessExpiresAt) < refreshEarly {
		if err := c.refresh(ctx); err != nil {
			return err
		}
	}
	req.Header.Set("Authorization", "Bearer "+c.creds.AccessToken)
	return nil
}

// refresh trades the refresh token for a new access token.
func (c *Client) refresh(ctx context.Context) error {
	if c.creds == nil || c.creds.RefreshToken == "" || time.Now().After(c.creds.RefreshExpiresAt) {
		return ErrNoCredentials
	}
	var t struct {
		AccessToken     string    `json:"accessToken"`
		AccessExpiresAt time.Time `json:"accessExpiresAt"`
	}
	if err := c.post(ctx, "/api/v1/auth/cli/refresh", map[string]string{"refreshToken": c.creds.RefreshToken}, &t); err != nil {
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusUnauthorized {
			return ErrNoCredentials
		}
		return err
	}
	c.creds.AccessToken, c.creds.AccessExpiresAt = t.AccessToken, t.AccessExpiresAt
	if c.save != nil {
		return c.save(*c.creds)
	}
	return nil
}

// post sends JSON without signing in: the sign-in endpoints.
func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gaia-Request", "1")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		var p server.Problem
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p)
		return &Error{Status: resp.StatusCode, Message: p.Error}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// New returns a client for the server at base.
func New(base string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Error is an error answer of the API.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%d: %s", e.Status, e.Message) }

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	resp, err := c.send(ctx, method, path)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		var p server.Problem
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p)
		return &Error{Status: resp.StatusCode, Message: p.Error}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// send makes a signed-in request; a 401 refreshes the access token once
// and tries again.
func (c *Client) send(ctx context.Context, method, path string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, nil)
		if err != nil {
			return nil, err
		}
		if err := c.authorize(ctx, req); err != nil {
			return nil, err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 || c.creds == nil {
			return resp, nil
		}
		_ = resp.Body.Close()
		if err := c.refresh(ctx); err != nil {
			return nil, err
		}
	}
}

// Runs lists runs, newest first; contract "" lists all.
func (c *Client) Runs(ctx context.Context, contract string, limit int) ([]server.Run, error) {
	q := url.Values{}
	if contract != "" {
		q.Set("contract", contract)
	}
	q.Set("limit", strconv.Itoa(limit))
	var out []server.Run
	return out, c.do(ctx, http.MethodGet, "/api/v1/runs?"+q.Encode(), &out)
}

// Run returns a run with its stages and record.
func (c *Client) Run(ctx context.Context, id string) (server.Run, error) {
	var out server.Run
	return out, c.do(ctx, http.MethodGet, "/api/v1/runs/"+url.PathEscape(id), &out)
}

// Cancel cancels a queued or running run.
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/runs/"+url.PathEscape(id)+"/cancel", nil)
}

// Watch streams a run's events after seq after to fn until the run ends,
// and returns its final status.
func (c *Client) Watch(ctx context.Context, id string, after int, fn func(server.Event) error) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/v1/runs/"+url.PathEscape(id)+"/events/stream?after="+strconv.Itoa(after), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/event-stream")
	if err := c.authorize(ctx, req); err != nil {
		return "", err
	}
	hc := *c.HTTP
	hc.Timeout = 0 // the stream lasts as long as the run
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var p server.Problem
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p)
		return "", &Error{Status: resp.StatusCode, Message: p.Error}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var name, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if name == "end" {
				var end struct{ Status string }
				_ = json.Unmarshal([]byte(data), &end)
				return end.Status, nil
			}
			if data != "" {
				var ev server.Event
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					return "", err
				}
				if err := fn(ev); err != nil {
					return "", err
				}
			}
			name, data = "", ""
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s: the stream ended before the run", id)
}

// Me is who the client is signed in as.
func (c *Client) Me(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	return out, c.do(ctx, http.MethodGet, "/api/v1/me", &out)
}

// Logout ends the CLI session on the server.
func (c *Client) Logout(ctx context.Context) error {
	if c.creds == nil {
		return nil
	}
	return c.post(ctx, "/api/v1/auth/cli/logout", map[string]string{"refreshToken": c.creds.RefreshToken}, nil)
}

// Get reads any API path into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, out)
}

// Post sends an API request without a body and reads its answer.
func (c *Client) Post(ctx context.Context, path string, _ any, out any) error {
	return c.do(ctx, http.MethodPost, path, out)
}
