package openbao

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client reads secrets from OpenBao's KV v2 engine.
type Client struct {
	addr    string
	token   string
	tokenFn func() string // a Session's current token, when set
	http    *http.Client
}

// New returns a client for OpenBao at addr with token.
func New(addr, token string) *Client {
	return &Client{addr: strings.TrimRight(addr, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

// Addr is OpenBao's address.
func (c *Client) Addr() string { return c.addr }

// Token is the token the client uses, for tools that talk to OpenBao
// themselves (Pulumi's secrets provider).
func (c *Client) Token() string {
	if c.tokenFn != nil {
		return c.tokenFn()
	}
	return c.token
}

// ParseRef splits bao://<mount>/<path> into mount and path.
func ParseRef(ref string) (mount, path string, err error) {
	rest, ok := strings.CutPrefix(ref, "bao://")
	if !ok {
		return "", "", fmt.Errorf("%q is not a bao:// reference", ref)
	}
	mount, path, ok = strings.Cut(rest, "/")
	if !ok || mount == "" || path == "" {
		return "", "", fmt.Errorf("%q: want bao://<mount>/<path>", ref)
	}
	return mount, path, nil
}

// Read returns the fields of the KV v2 secret ref points to.
func (c *Client) Read(ctx context.Context, ref string) (map[string]string, error) {
	mount, path, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/%s/data/%s", c.addr, mount, path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", c.Token())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openbao: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s: no such secret", ref)
	case http.StatusForbidden:
		return nil, fmt.Errorf("%s: permission denied", ref)
	default:
		return nil, fmt.Errorf("%s: openbao status %d", ref, resp.StatusCode)
	}
	var out struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	fields := map[string]string{}
	for k, v := range out.Data.Data {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: field %s is not a string", ref, k)
		}
		fields[k] = s
	}
	return fields, nil
}

// Field reads one field of a secret.
func (c *Client) Field(ctx context.Context, ref, field string) (string, error) {
	f, err := c.Read(ctx, ref)
	if err != nil {
		return "", err
	}
	v, ok := f[field]
	if !ok {
		return "", fmt.Errorf("%s: no field %q", ref, field)
	}
	return v, nil
}
