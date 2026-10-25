package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/repo"
)

func TestCatalogAPI(t *testing.T) {
	e := setup(t)
	cat := &CatalogSource{Repos: e.repos, Platform: repo.Rev{Name: "platform", URL: e.repo}}
	if err := cat.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{Listen: "127.0.0.1:0", Catalog: cat}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	var list struct {
		Commit  string
		Objects []CatalogObject
	}
	if code := get(t, hs.URL+"/api/v1/catalog", &list); code != 200 || list.Commit == "" || len(list.Objects) == 0 {
		t.Fatalf("catalog: %d %+v", code, list)
	}
	for _, o := range list.Objects {
		if o.Kind == "Server" {
			t.Errorf("a contract in the catalog: %+v", o)
		}
	}
	var nets struct{ Objects []CatalogObject }
	if get(t, hs.URL+"/api/v1/catalog?kind=Network", &nets); len(nets.Objects) == 0 || nets.Objects[0].Kind != "Network" || nets.Objects[0].UsedBy == 0 {
		t.Fatalf("networks: %+v", nets)
	}
	var d CatalogDetail
	if code := get(t, hs.URL+"/api/v1/catalog/Network/"+nets.Objects[0].Name, &d); code != 200 || !strings.Contains(d.YAML, "kind: Network") || len(d.UsedByRefs) == 0 || d.Source == "" {
		t.Errorf("network: %d %+v", code, d)
	}
	used := false
	for _, u := range d.UsedByRefs {
		used = used || (u.Kind == "Server" && u.Name == "poc-web")
	}
	if !used {
		t.Errorf("poc-web does not use %s: %+v", d.Name, d.UsedByRefs)
	}
	if code := get(t, hs.URL+"/api/v1/catalog/Server/poc-web", nil); code != 404 {
		t.Errorf("a contract through the catalog: %d", code)
	}
	var ps []ProviderInfo
	if code := get(t, hs.URL+"/api/v1/providers", &ps); code != 200 || len(ps) == 0 || ps[0].Type == "" {
		t.Errorf("providers: %d %+v", code, ps)
	}
}
