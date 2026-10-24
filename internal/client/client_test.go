package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/server"
)

func TestWatchReadsEventsUntilTheEnd(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "3" {
			t.Errorf("after = %q", r.URL.Query().Get("after"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "id: 4\nevent: log\ndata: {\"seq\":4,\"type\":\"log\",\"stdout\":\"pulumi up\"}\n\n")
		_, _ = fmt.Fprint(w, ": keepalive\n\n")
		_, _ = fmt.Fprint(w, "id: 5\nevent: ansible\ndata: {\"seq\":5,\"type\":\"runner_on_ok\",\"host\":\"u1\",\"task\":\"Motd\",\"changed\":true}\n\n")
		_, _ = fmt.Fprint(w, "event: end\ndata: {\"status\":\"succeeded\"}\n\n")
	}))
	defer hs.Close()
	var got []server.Event
	status, err := New(hs.URL).Watch(context.Background(), "run-x", 3, func(e server.Event) error {
		got = append(got, e)
		return nil
	})
	if err != nil || status != "succeeded" {
		t.Fatalf("status %q, %v", status, err)
	}
	if len(got) != 2 || got[0].Stdout != "pulumi up" || got[1].Task != "Motd" || !got[1].Changed {
		t.Errorf("events = %+v", got)
	}
}

func TestWatchFailsWhenTheStreamBreaks(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "id: 1\nevent: log\ndata: {\"seq\":1,\"type\":\"log\"}\n\n")
	}))
	defer hs.Close()
	if _, err := New(hs.URL).Watch(context.Background(), "run-x", 0, func(server.Event) error { return nil }); err == nil {
		t.Error("no error for a stream that ended early")
	}
}

func TestErrorsCarryTheProblem(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"error":"run has already finished"}`)
	}))
	defer hs.Close()
	err := New(hs.URL).Cancel(context.Background(), "run-x")
	if e, ok := err.(*Error); !ok || e.Status != 409 || e.Message != "run has already finished" {
		t.Errorf("err = %#v", err)
	}
}

// fakeAuth is the server side of CLI sign-in: it plays the browser's
// approval and checks PKCE when the code is traded.
type fakeAuth struct {
	t         *testing.T
	challenge string
	redirect  string
	refreshes int
	access    string
}

func (f *fakeAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v1/auth/cli/token":
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		sum := sha256.Sum256([]byte(b["codeVerifier"]))
		if b["code"] != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || b["redirectUri"] != f.redirect || r.Header.Get("X-Gaia-Request") != "1" {
			w.WriteHeader(401)
			_, _ = fmt.Fprint(w, `{"error":"the code is not valid"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "a1", "accessExpiresAt": time.Now().Add(time.Hour), "refreshToken": "r1", "refreshExpiresAt": time.Now().Add(12 * time.Hour)})
	case "/api/v1/auth/cli/refresh":
		f.refreshes++
		f.access = fmt.Sprintf("a%d", f.refreshes+1)
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": f.access, "accessExpiresAt": time.Now().Add(time.Hour)})
	case "/api/v1/runs/run-x":
		if r.Header.Get("Authorization") != "Bearer "+f.access {
			w.WriteHeader(401)
			_, _ = fmt.Fprint(w, `{"error":"sign in first"}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"id":"run-x","status":"succeeded"}`)
	default:
		http.NotFound(w, r)
	}
}

func TestLoopbackLogin(t *testing.T) {
	f := &fakeAuth{t: t}
	hs := httptest.NewServer(f)
	defer hs.Close()
	c := New(hs.URL)
	// open plays the browser: it reads the request, "approves" it and
	// follows the redirect back to the CLI.
	open := func(u string) error {
		p, _ := url.Parse(u)
		q := p.Query()
		f.challenge, f.redirect = q.Get("code_challenge"), q.Get("redirect_uri")
		if !strings.HasPrefix(f.redirect, "http://127.0.0.1:") || p.Path != "/cli-login" {
			t.Errorf("login URL %s", u)
		}
		go func() {
			resp, err := http.Get(f.redirect + "?code=the-code&state=" + url.QueryEscape(q.Get("state")))
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	cr, err := c.Login(context.Background(), open, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cr.AccessToken != "a1" || cr.RefreshToken != "r1" || cr.Server != hs.URL {
		t.Errorf("credentials = %+v", cr)
	}
}

func TestA401RefreshesOnce(t *testing.T) {
	f := &fakeAuth{t: t, access: "a-valid"}
	hs := httptest.NewServer(f)
	defer hs.Close()
	var saved []Credentials
	c := New(hs.URL).WithCredentials(Credentials{Server: hs.URL, AccessToken: "stale", AccessExpiresAt: time.Now().Add(time.Hour), RefreshToken: "r1", RefreshExpiresAt: time.Now().Add(time.Hour)},
		func(cr Credentials) error { saved = append(saved, cr); return nil })
	r, err := c.Run(context.Background(), "run-x")
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("run: %+v %v", r, err)
	}
	if f.refreshes != 1 || len(saved) != 1 || saved[0].AccessToken != f.access {
		t.Errorf("refreshes %d, saved %+v", f.refreshes, saved)
	}
	// An access token about to expire is renewed before the request.
	c.creds.AccessExpiresAt = time.Now().Add(10 * time.Second)
	if _, err := c.Run(context.Background(), "run-x"); err != nil || f.refreshes != 2 {
		t.Errorf("early refresh: %d %v", f.refreshes, err)
	}
	// Without a usable refresh token the caller is told to sign in.
	c.creds.RefreshExpiresAt = time.Now().Add(-time.Minute)
	c.creds.AccessExpiresAt = time.Now()
	if _, err := c.Run(context.Background(), "run-x"); err != ErrNoCredentials {
		t.Errorf("expired session: %v", err)
	}
}

func TestDeviceLoginPolls(t *testing.T) {
	polls := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/device":
			_, _ = fmt.Fprint(w, `{"deviceCode":"d","userCode":"BCDF-GHJK","verificationUri":"http://x/device","interval":0,"expiresIn":30}`)
		case "/api/v1/auth/device/token":
			polls++
			if polls < 3 {
				w.WriteHeader(400)
				_, _ = fmt.Fprint(w, `{"error":"authorization_pending"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"accessToken":"a","refreshToken":"r"}`)
		}
	}))
	defer hs.Close()
	var out strings.Builder
	cr, err := New(hs.URL).DeviceLogin(context.Background(), &out)
	if err != nil || cr.RefreshToken != "r" || polls != 3 || !strings.Contains(out.String(), "BCDF-GHJK") {
		t.Errorf("device login: %+v %v, %d polls, %q", cr, err, polls, out.String())
	}
}
