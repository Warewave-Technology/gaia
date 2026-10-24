package ipam

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Warewave-Technology/gaia/internal/ipam"
)

// Client talks to gaia-ipam.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// New returns a client for the IPAM at endpoint.
func New(endpoint, token string) *Client {
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// SetPool creates or updates a pool.
func (c *Client) SetPool(ctx context.Context, p ipam.Pool) error {
	return c.do(ctx, http.MethodPost, "/v1/pools", p, nil)
}

// Allocate returns owner's address in pool, allocating one if needed.
// Pools lists the pools with what they are for.
func (c *Client) Pools(ctx context.Context) ([]ipam.Pool, error) {
	var out []ipam.Pool
	return out, c.do(ctx, http.MethodGet, "/v1/pools", nil, &out)
}

func (c *Client) Allocate(ctx context.Context, pool, owner, hostname string) (ipam.Allocation, error) {
	var a ipam.Allocation
	err := c.do(ctx, http.MethodPost, "/v1/allocations", ipam.AllocateRequest{Pool: pool, Owner: owner, Hostname: hostname}, &a)
	return a, err
}

// Allocations returns owner's live addresses.
func (c *Client) Allocations(ctx context.Context, owner string) ([]ipam.Allocation, error) {
	var a []ipam.Allocation
	err := c.do(ctx, http.MethodGet, "/v1/allocations?owner="+url.QueryEscape(owner), nil, &a)
	return a, err
}

// Release frees owner's addresses.
func (c *Client) Release(ctx context.Context, owner string) ([]ipam.Allocation, error) {
	var a []ipam.Allocation
	err := c.do(ctx, http.MethodDelete, "/v1/allocations?owner="+url.QueryEscape(owner), nil, &a)
	return a, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ipam: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return fmt.Errorf("ipam %s %s: %s (%d)", method, path, e.Error, resp.StatusCode)
		}
		return fmt.Errorf("ipam %s %s: status %d", method, path, resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
