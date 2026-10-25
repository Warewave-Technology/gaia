package auth

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSignInPages(t *testing.T) {
	e := setup(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := noRedirect.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	for _, p := range []string{"/login", "/enroll", "/device", "/cli-login"} {
		resp, body := get(p)
		csp := resp.Header.Get("Content-Security-Policy")
		if resp.StatusCode != 200 || !strings.Contains(body, `href="/auth/gaia-auth.css"`) || strings.Contains(body, "style=") ||
			!strings.Contains(csp, "style-src 'self';") || strings.Contains(csp, "unsafe-inline") {
			t.Errorf("%s: %d csp=%q", p, resp.StatusCode, csp)
		}
	}
	if resp, body := get("/auth/gaia-auth.css"); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") || !strings.Contains(body, "#fabd2f") {
		t.Errorf("css: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, body := get("/auth/gaia-auth.js"); resp.StatusCode != 200 || strings.Contains(body, ".style.") {
		t.Errorf("js sets styles inline: %d", resp.StatusCode)
	}
	// Old notification links lead to the change in the UI.
	if resp, _ := get("/approve/chg-1"); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/changes/chg-1" {
		t.Errorf("approve link: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}
