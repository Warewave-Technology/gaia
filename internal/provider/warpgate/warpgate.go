// Package warpgate keeps servers in Warpgate, the bastion (design 14.4):
// one target per server, Gaia's roles on them, the people of a contract's
// access in those roles, and the servers' SSH host keys as Gaia recorded
// them. Gaia never creates Warpgate users; they come from its SSO or LDAP.
package warpgate

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// RolePrefix marks the roles Gaia manages; it touches no other.
const RolePrefix = "gaia/"

// Client talks to Warpgate's admin API with an API token.
type Client struct {
	base, token string
	http        *http.Client
}

// New returns a client for a Warpgate at endpoint (https://host:8888).
// Warpgate's own certificate is often self-signed: ca trusts it, insecure
// skips the check.
func New(endpoint, token string, insecure bool, ca []byte) (*Client, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure} //nolint:gosec // the operator's choice, for self-signed bastions
	if len(ca) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca) {
			return nil, errors.New("warpgate: ca: no PEM certificate")
		}
		tc.RootCAs = pool
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("warpgate: endpoint %q: want https://host:port", endpoint)
	}
	return &Client{base: strings.TrimSuffix(endpoint, "/") + "/@warpgate/admin/api", token: token,
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tc}}}, nil
}

// ErrNotFound: Warpgate has no such object.
var ErrNotFound = errors.New("warpgate: not found")

// ErrTaken: a target of that name is not the server's.
var ErrTaken = errors.New("warpgate: the target name is taken")

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Warpgate-Token", c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("warpgate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s %s", ErrNotFound, method, path)
	case resp.StatusCode >= 300:
		return fmt.Errorf("warpgate %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("warpgate %s %s: %w", method, path, err)
	}
	return nil
}

// Target is a server in Warpgate. Kind is "ssh" or "rdp".
type Target struct {
	ID          string
	Name        string
	Description string
	Kind        string
	Host        string
	Port        int
	Username    string // SSH: the account Warpgate logs in as
}

type apiTarget struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Options     map[string]any `json:"options"`
	// Gaia's targets take no tickets and need no approval: access is
	// approved in Gaia (design 14.3).
	TicketRequestsDisabled bool `json:"ticket_requests_disabled"`
	TicketRequireApproval  bool `json:"ticket_require_approval"`
	RequireApproval        bool `json:"require_approval"`
}

func (t Target) api() apiTarget {
	o := map[string]any{"host": t.Host, "port": t.Port}
	switch t.Kind {
	case "rdp":
		// The target's own sign-in screen: people log on with their
		// directory accounts, Warpgate keeps no password.
		o["kind"], o["username"], o["auth"] = "Rdp", "", map[string]any{"kind": "Password", "password": ""}
		o["verify_tls"], o["compression"], o["interactive_logon"], o["tls_security"] = false, "remotefx", true, "Tls12"
	default:
		o["kind"], o["username"], o["allow_insecure_algos"] = "Ssh", t.Username, false
		o["auth"] = map[string]any{"kind": "PublicKey"}
	}
	return apiTarget{Name: t.Name, Description: t.Description, Options: o, TicketRequestsDisabled: true}
}

func (a apiTarget) target() Target {
	t := Target{ID: a.ID, Name: a.Name, Description: a.Description}
	t.Kind = strings.ToLower(fmt.Sprint(a.Options["kind"]))
	t.Host, _ = a.Options["host"].(string)
	if p, ok := a.Options["port"].(float64); ok {
		t.Port = int(p)
	}
	t.Username, _ = a.Options["username"].(string)
	return t
}

// Targets lists every target.
func (c *Client) Targets(ctx context.Context) ([]Target, error) {
	var ts []apiTarget
	if err := c.do(ctx, http.MethodGet, "/targets", nil, &ts); err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.target())
	}
	return out, nil
}

// Result is what EnsureTarget did: create, update or same.
type Result struct {
	ID     string
	Action string
}

// EnsureTarget makes the server's target as t says. The target is found
// by its recorded ID; without one, a target of the name is the server's
// only when its description carries mark (its gaia.uid).
func (c *Client) EnsureTarget(ctx context.Context, t Target, id, mark string) (Result, error) {
	var cur apiTarget
	if id != "" {
		err := c.do(ctx, http.MethodGet, "/targets/"+id, nil, &cur)
		if errors.Is(err, ErrNotFound) {
			id = ""
		} else if err != nil {
			return Result{}, err
		}
	}
	if id == "" {
		all, err := c.Targets(ctx)
		if err != nil {
			return Result{}, err
		}
		for _, x := range all {
			if x.Name != t.Name {
				continue
			}
			if mark == "" || !strings.Contains(x.Description, mark) {
				return Result{}, fmt.Errorf("%w: %s is not the server's", ErrTaken, t.Name)
			}
			id = x.ID
			if err := c.do(ctx, http.MethodGet, "/targets/"+id, nil, &cur); err != nil {
				return Result{}, err
			}
		}
	}
	if id == "" {
		var made apiTarget
		if err := c.do(ctx, http.MethodPost, "/targets", t.api(), &made); err != nil {
			return Result{}, err
		}
		return Result{ID: made.ID, Action: "create"}, nil
	}
	got := cur.target()
	t.ID = id
	if got == t && cur.TicketRequestsDisabled {
		return Result{ID: id, Action: "same"}, nil
	}
	if err := c.do(ctx, http.MethodPut, "/targets/"+id, t.api(), nil); err != nil {
		return Result{}, err
	}
	return Result{ID: id, Action: "update"}, nil
}

// DeleteTarget deletes a target; false when it was already gone.
func (c *Client) DeleteTarget(ctx context.Context, id string) (bool, error) {
	err := c.do(ctx, http.MethodDelete, "/targets/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Role is a Warpgate role.
type Role struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// EnsureRole returns the role of the name, made when missing.
func (c *Client) EnsureRole(ctx context.Context, name string) (string, error) {
	var rs []Role
	if err := c.do(ctx, http.MethodGet, "/roles?search="+url.QueryEscape(name), nil, &rs); err != nil {
		return "", err
	}
	for _, r := range rs {
		if r.Name == name {
			return r.ID, nil
		}
	}
	var made Role
	if err := c.do(ctx, http.MethodPost, "/roles", map[string]any{"name": name, "description": "Managed by Gaia."}, &made); err != nil {
		return "", err
	}
	return made.ID, nil
}

// Roles lists the roles Gaia manages.
func (c *Client) Roles(ctx context.Context) ([]Role, error) {
	var rs []Role
	if err := c.do(ctx, http.MethodGet, "/roles?search="+url.QueryEscape(RolePrefix), nil, &rs); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(rs, func(r Role) bool { return !strings.HasPrefix(r.Name, RolePrefix) }), nil
}

// SetTargetRoles gives a target exactly the roles Gaia names (by ID) among
// those Gaia manages; other roles stay. It reports what changed.
func (c *Client) SetTargetRoles(ctx context.Context, target string, roles []string) ([]string, error) {
	var cur []Role
	if err := c.do(ctx, http.MethodGet, "/targets/"+target+"/roles", nil, &cur); err != nil {
		return nil, err
	}
	var changed []string
	for _, id := range roles {
		if !slices.ContainsFunc(cur, func(r Role) bool { return r.ID == id }) {
			if err := c.do(ctx, http.MethodPost, "/targets/"+target+"/roles/"+id, nil, nil); err != nil {
				return changed, err
			}
			changed = append(changed, "+"+id)
		}
	}
	for _, r := range cur {
		if strings.HasPrefix(r.Name, RolePrefix) && !slices.Contains(roles, r.ID) {
			if err := c.do(ctx, http.MethodDelete, "/targets/"+target+"/roles/"+r.ID, nil, nil); err != nil {
				return changed, err
			}
			changed = append(changed, "-"+r.Name)
		}
	}
	return changed, nil
}

// User is a Warpgate user.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// Users lists every user.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var us []User
	return us, c.do(ctx, http.MethodGet, "/users", nil, &us)
}

// SetRoleUsers makes a role's users exactly the given ones (by ID), and
// reports who was added and removed (by username).
func (c *Client) SetRoleUsers(ctx context.Context, role string, users []User) (added, removed []string, err error) {
	var cur []User
	if err := c.do(ctx, http.MethodGet, "/role/"+role+"/users", nil, &cur); err != nil {
		return nil, nil, err
	}
	for _, u := range users {
		if !slices.ContainsFunc(cur, func(x User) bool { return x.ID == u.ID }) {
			if err := c.do(ctx, http.MethodPost, "/users/"+u.ID+"/roles/"+role, map[string]any{}, nil); err != nil {
				return added, removed, err
			}
			added = append(added, u.Username)
		}
	}
	for _, u := range cur {
		if !slices.ContainsFunc(users, func(x User) bool { return x.ID == u.ID }) {
			if err := c.do(ctx, http.MethodDelete, "/users/"+u.ID+"/roles/"+role, nil, nil); err != nil {
				return added, removed, err
			}
			removed = append(removed, u.Username)
		}
	}
	return added, removed, nil
}

// OwnKeys are the public keys Warpgate logs in to targets with, in
// authorized_keys form.
func (c *Client) OwnKeys(ctx context.Context) ([]string, error) {
	var ks []struct {
		PublicKey string `json:"public_key"`
	}
	if err := c.do(ctx, http.MethodGet, "/ssh/own-keys", nil, &ks); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		out = append(out, strings.TrimSpace(k.PublicKey))
	}
	return out, nil
}

type knownHost struct {
	ID      string `json:"id,omitempty"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	KeyType string `json:"key_type"`
	Key     string `json:"key_base64"`
}

// SetHostKeys makes the keys Warpgate trusts for host:port exactly keys:
// an address can come back with another server (design 9.3). It reports
// whether anything changed.
func (c *Client) SetHostKeys(ctx context.Context, host string, port int, keys []ssh.PublicKey) (bool, error) {
	var cur []knownHost
	if err := c.do(ctx, http.MethodGet, "/ssh/known-hosts", nil, &cur); err != nil {
		return false, err
	}
	want := make([]knownHost, 0, len(keys))
	for _, k := range keys {
		want = append(want, knownHost{Host: host, Port: port, KeyType: k.Type(), Key: base64.StdEncoding.EncodeToString(k.Marshal())})
	}
	same := func(a, b knownHost) bool {
		return a.Host == b.Host && a.Port == b.Port && a.KeyType == b.KeyType && a.Key == b.Key
	}
	changed := false
	for _, k := range cur {
		if k.Host == host && k.Port == port && !slices.ContainsFunc(want, func(w knownHost) bool { return same(w, k) }) {
			if err := c.do(ctx, http.MethodDelete, "/ssh/known-hosts/"+k.ID, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
				return changed, err
			}
			changed = true
		}
	}
	for _, w := range want {
		if !slices.ContainsFunc(cur, func(k knownHost) bool { return same(w, k) }) {
			if err := c.do(ctx, http.MethodPost, "/ssh/known-hosts", w, nil); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	return changed, nil
}
