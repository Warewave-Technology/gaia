package proxmox

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.creds.Endpoint, "/")+"/api2/json"+path, body)
	if err != nil {
		return err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.creds.APIToken())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("proxmox: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("proxmox %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// node returns the node a VM runs on.
func (c *Client) node(ctx context.Context, vmid int) (string, error) {
	var out struct {
		Data []struct {
			VMID int    `json:"vmid"`
			Node string `json:"node"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/cluster/resources?type=vm", &out); err != nil {
		return "", err
	}
	for _, r := range out.Data {
		if r.VMID == vmid {
			return r.Node, nil
		}
	}
	return "", fmt.Errorf("proxmox: no VM %d", vmid)
}

// SnapshotName makes a Proxmox snapshot name: a letter first, then
// letters, digits and _, at most 40 characters.
func SnapshotName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := b.String()
	if name == "" || (name[0] < 'a' || name[0] > 'z') && (name[0] < 'A' || name[0] > 'Z') {
		name = "g" + name
	}
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

// Snapshot takes a snapshot of a VM (without its memory) and waits for
// the task to end.
func (c *Client) Snapshot(ctx context.Context, vmid int, name, description string) error {
	node, err := c.node(ctx, vmid)
	if err != nil {
		return err
	}
	var started struct {
		Data string `json:"data"` // the task's UPID
	}
	form := url.Values{"snapname": {name}, "description": {description}, "vmstate": {"0"}}
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/nodes/%s/qemu/%d/snapshot", node, vmid), form, &started); err != nil {
		return err
	}
	return c.wait(ctx, node, started.Data)
}

// wait waits for a task to stop and returns its error.
func (c *Client) wait(ctx context.Context, node, upid string) error {
	for {
		var st struct {
			Data struct {
				Status     string `json:"status"`
				ExitStatus string `json:"exitstatus"`
			} `json:"data"`
		}
		if err := c.get(ctx, fmt.Sprintf("/nodes/%s/tasks/%s/status", node, url.PathEscape(upid)), &st); err != nil {
			return err
		}
		if st.Data.Status == "stopped" {
			if st.Data.ExitStatus != "OK" {
				return fmt.Errorf("proxmox task %s: %s", upid, st.Data.ExitStatus)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
