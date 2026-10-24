package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Credentials reach the Proxmox API with an API token.
type Credentials struct {
	Endpoint    string // https://host:8006
	TokenID     string // user@realm!name
	TokenSecret string
	Insecure    bool // self-signed certificate
}

// APIToken is the token in the form the Proxmox API and the Pulumi
// provider take: user@realm!name=secret.
func (c Credentials) APIToken() string { return c.TokenID + "=" + c.TokenSecret }

// Client is the part of the Proxmox API Gaia uses directly.
type Client struct {
	creds Credentials
	http  *http.Client
}

// New returns a client for the Proxmox API.
func New(c Credentials) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // lab Proxmox with a self-signed certificate
	}
	return &Client{creds: c, http: &http.Client{Timeout: 30 * time.Second, Transport: tr}}
}

// UsedVMIDs returns every VM and container ID of the cluster, Gaia's or not.
func (c *Client) UsedVMIDs(ctx context.Context) (map[int]bool, error) {
	var out struct {
		Data []struct {
			VMID int `json:"vmid"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/cluster/resources?type=vm", &out); err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, r := range out.Data {
		used[r.VMID] = true
	}
	return used, nil
}

// FreeVMID returns the lowest ID in [start, end] that is neither used in
// the cluster nor in taken.
func FreeVMID(used, taken map[int]bool, start, end int) (int, error) {
	for id := start; id <= end; id++ {
		if !used[id] && !taken[id] {
			return id, nil
		}
	}
	return 0, fmt.Errorf("no free VM ID in %d-%d", start, end)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.creds.Endpoint, "/")+"/api2/json"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.creds.APIToken())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxmox GET %s: %d %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}
