// Package client talks to gaia server's API (api/openapi.yaml).
package client

import (
	"bufio"
	"context"
	"encoding/json"
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
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, nil)
	if err != nil {
		return err
	}
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
