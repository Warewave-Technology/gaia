package nexus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fake answers as Nexus 3 does: the search API with checksums, and the
// repository paths.
func fake(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "gaia" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/service/rest/v1/search/assets":
			if r.URL.Query().Get("repository") == "releases" && r.URL.Query().Get("name") == "portal/portal-2.4.1.tar.gz" {
				_, _ = w.Write([]byte(`{"items":[{"path":"portal/portal-2.4.1.tar.gz","downloadUrl":"https://nexus/repository/releases/portal/portal-2.4.1.tar.gz",
					"checksum":{"sha1":"x","sha256":"d730d8f9"}}],"continuationToken":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[],"continuationToken":null}`))
		case r.URL.Path == "/repository/maven/com/corp/app-1.jar" && r.Method == http.MethodHead:
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestAsset(t *testing.T) {
	s := fake(t)
	c := New(s.URL+"/", "gaia", "pw")
	ctx := context.Background()
	a, err := c.Asset(ctx, "releases", "/portal/portal-2.4.1.tar.gz")
	if err != nil || a != (Asset{URL: "https://nexus/repository/releases/portal/portal-2.4.1.tar.gz", SHA256: "d730d8f9"}) {
		t.Errorf("asset = %+v %v", a, err)
	}
	// Found by asking for the file: no checksum.
	if a, err := c.Asset(ctx, "maven", "com/corp/app-1.jar"); err != nil || a != (Asset{URL: s.URL + "/repository/maven/com/corp/app-1.jar"}) {
		t.Errorf("by HEAD = %+v %v", a, err)
	}
	if _, err := c.Asset(ctx, "releases", "portal/portal-9.tar.gz"); !errors.Is(err, ErrNoArtifact) {
		t.Errorf("missing: %v", err)
	}
	if _, err := New(s.URL, "", "").Asset(ctx, "releases", "x"); err == nil {
		t.Error("anonymous read of a closed Nexus")
	}
}
