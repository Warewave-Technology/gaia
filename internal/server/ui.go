package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/Warewave-Technology/gaia/web"
)

// The web UI (T20): the built single-page app in web/dist, embedded. A path with a file serves that file; any other
// path without an extension is a route of the app and gets index.html; the rest is 404. The app talks to /api/v1
// with the session cookie.

// uiCSP lets the page load only what Gaia itself serves: no CDN, no inline script, nothing framed.
const uiCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func uiHandler() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees dist
	}
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if fi, err := fs.Stat(dist, p); err == nil && !fi.IsDir() {
				if strings.HasPrefix(p, "assets/") || strings.HasPrefix(p, "fonts/") {
					h.Set("Cache-Control", "public, max-age=31536000, immutable") // hashed names
				} else {
					h.Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(p) != "" {
				http.NotFound(w, r)
				return
			}
		}
		b, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("This gaia binary was built without its web UI (run make web, then build again). The API is at /api/v1.\n"))
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		w.Write(b)
	})
}

// sessionCookie carries a browser's session. __Host-: Secure, Path=/, no Domain; HttpOnly and SameSite=Strict.
const sessionCookie = "__Host-gaia_session"
