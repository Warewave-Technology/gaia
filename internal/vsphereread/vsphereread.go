// Package vsphereread is Gaia's read-only vSphere client for the migration check
// (ADR-003 §2: never a write API against a migration source).
//
// Read-only is enforced at one choke point, the HTTP transport under govmomi's
// SOAP client, so it holds whatever code builds the request:
//
//   - POST /sdk: the SOAP body is parsed and its operation must be on ReadOps;
//     anything else is refused before it leaves the process.
//   - GET or HEAD /folder/...: datastore file reads.
//   - Every other method or path is refused.
//
// Every request, allowed or refused, is recorded in the call log, which is the
// run's evidence that no write call reached the host.
package vsphereread

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
)

// ReadOps is the allowlist of SOAP operations: service content, session login and
// logout, and property collector reads. Nothing on it changes the host's state
// beyond the session itself.
var ReadOps = map[string]bool{
	"RetrieveServiceContent":       true,
	"Login":                        true,
	"Logout":                       true,
	"CurrentTime":                  true,
	"RetrieveProperties":           true,
	"RetrievePropertiesEx":         true,
	"ContinueRetrievePropertiesEx": true,
	"CancelRetrievePropertiesEx":   true,
}

// ErrBlocked is returned for a request the guard refused; it never reached the host.
var ErrBlocked = errors.New("blocked by Gaia's read-only guard")

// Call is one request seen by the guard.
type Call struct {
	Time    time.Time `json:"time"`
	Method  string    `json:"method"`
	Path    string    `json:"path"`
	Op      string    `json:"op,omitempty"` // SOAP operation
	Allowed bool      `json:"allowed"`
	Status  int       `json:"status,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Guard is an http.RoundTripper that lets only read requests through.
type Guard struct {
	next  http.RoundTripper
	mu    sync.Mutex
	calls []Call
	log   io.Writer // JSON lines, if set
}

// NewGuard wraps next.
func NewGuard(next http.RoundTripper, log io.Writer) *Guard { return &Guard{next: next, log: log} }

// Calls returns a copy of the calls so far.
func (g *Guard) Calls() []Call {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Call(nil), g.calls...)
}

func (g *Guard) record(c Call) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, c)
	if g.log != nil {
		b, _ := json.Marshal(c)
		g.log.Write(append(b, '\n'))
	}
}

// RoundTrip implements http.RoundTripper.
func (g *Guard) RoundTrip(req *http.Request) (*http.Response, error) {
	c := Call{Time: time.Now().UTC(), Method: req.Method, Path: req.URL.Path}
	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/sdk":
		var body []byte
		if req.Body != nil {
			var err error
			if body, err = io.ReadAll(req.Body); err != nil {
				return nil, err
			}
			req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		c.Op = SOAPOperation(body)
		c.Allowed = ReadOps[c.Op]
	case (req.Method == http.MethodGet || req.Method == http.MethodHead) && strings.HasPrefix(req.URL.Path, "/folder/"):
		c.Allowed = true
	}
	if !c.Allowed {
		c.Error = ErrBlocked.Error()
		g.record(c)
		what := c.Op
		if what == "" {
			what = req.Method + " " + req.URL.Path
		}
		return nil, fmt.Errorf("%s: %w", what, ErrBlocked)
	}
	res, err := g.next.RoundTrip(req)
	if err != nil {
		c.Error = err.Error()
	} else {
		c.Status = res.StatusCode
	}
	g.record(c)
	return res, err
}

// SOAPOperation returns the local name of the first element in the SOAP Body, or "".
func SOAPOperation(body []byte) string {
	d := xml.NewDecoder(bytes.NewReader(body))
	inBody := false
	for {
		t, err := d.Token()
		if err != nil {
			return ""
		}
		if se, ok := t.(xml.StartElement); ok {
			if inBody {
				return se.Name.Local
			}
			inBody = se.Name.Local == "Body"
		}
	}
}

// Client is a logged-in, guarded vSphere session.
type Client struct {
	Vim   *vim25.Client
	soap  *soap.Client
	Guard *Guard
	Host  string
}

// Connect logs in to rawURL (https://host/sdk) through the guard. Nothing reaches
// the host before the guard is in place: the service content is the first request.
func Connect(ctx context.Context, rawURL, user, password string, insecure bool, callLog io.Writer) (*Client, error) {
	u, err := soap.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	sc := soap.NewClient(u, insecure)
	g := NewGuard(sc.Client.Transport, callLog)
	sc.Client.Transport = g
	vc, err := vim25.NewClient(ctx, sc)
	if err != nil {
		return nil, fmt.Errorf("vSphere %s: %w", u.Host, err)
	}
	if err := session.NewManager(vc).Login(ctx, url.UserPassword(user, password)); err != nil {
		return nil, fmt.Errorf("vSphere login as %s: %w", user, err)
	}
	return &Client{Vim: vc, soap: sc, Guard: g, Host: u.Host}, nil
}

// Logout ends the session.
func (c *Client) Logout(ctx context.Context) error { return session.NewManager(c.Vim).Logout(ctx) }
