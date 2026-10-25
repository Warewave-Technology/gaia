package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
	return w
}

func TestServesTheApp(t *testing.T) {
	h := New(fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>Gaia</title>")},
		"favicon.svg":        {Data: []byte("<svg/>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
	})
	for _, p := range []string{"/", "/changes/42", "/contracts/poc-web", "/index.html"} {
		w := get(t, h, p)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Gaia</title>") || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q %q", p, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("%s: no CSP", p)
		}
	}
	if w := get(t, h, "/assets/app-1a2b.js"); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if w := get(t, h, "/assets/gone-9z.js"); w.Code != 404 {
		t.Errorf("a missing asset is not the app: %d", w.Code)
	}
	if w := get(t, h, "/api/v1/nothing"); w.Code != 404 || !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("api: %d %q", w.Code, w.Body.String())
	}
}

func TestWithoutABuild(t *testing.T) {
	if w := get(t, New(fstest.MapFS{".keep": {}}), "/"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "make web") {
		t.Errorf("%d %q", w.Code, w.Body.String())
	}
}
