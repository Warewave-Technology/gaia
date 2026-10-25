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
	e.git.Write("platform/roles.yaml", `apiVersion: gaia/v1alpha1
kind: AnsibleRole
metadata: { name: nginx, labels: { category: web } }
spec:
  source: { provider: config-repo, role: nginx }
  os: [el]
  params:
    server_name: { type: str, required: true, description: [The site's name., nginx serves it.], gaia: { pattern: '^[a-z.]+$' } }
    listen_port: { type: int, default: 80, gaia: { min: 1, max: 65535 } }
    tls:
      type: dict
      options:
        cert: { type: path, required: true }
        key: { type: str, no_log: true, default: secret }
  rules:
    - { rule: 'params.listen_port != 22', message: port 22 is SSH's }
`)
	e.git.Commit("a role")
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
	if get(t, hs.URL+"/api/v1/catalog?kind=Network", &nets); len(nets.Objects) == 0 || nets.Objects[0].Kind != "Network" || nets.Objects[0].UsedBy == 0 || nets.Objects[0].Contracts != 1 {
		t.Fatalf("networks: %+v", nets)
	}
	var install struct{ Objects []CatalogObject }
	get(t, hs.URL+"/api/v1/catalog?kind=AnsibleRole,Size&spec=1", &install)
	kinds := map[string]bool{}
	for _, o := range install.Objects {
		kinds[o.Kind] = true
		if o.Kind == "AnsibleRole" && o.Name == "nginx" && (o.Params != 3 || o.Spec["params"] != nil || o.Spec["os"] == nil) {
			t.Errorf("nginx with its spec: %+v", o)
		}
		if o.Kind == "Size" && o.Spec["cpu"] == nil {
			t.Errorf("size without its spec: %+v", o)
		}
	}
	if len(kinds) != 2 || !kinds["AnsibleRole"] || !kinds["Size"] {
		t.Errorf("two kinds: %v", kinds)
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
	var role CatalogDetail
	if code := get(t, hs.URL+"/api/v1/catalog/AnsibleRole/nginx", &role); code != 200 || role.Role == nil || role.Role.Role != "nginx" || strings.Join(role.Role.OS, ",") != "el" {
		t.Fatalf("role: %d %+v", code, role)
	}
	names := []string{}
	for _, p := range role.Params {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "listen_port,server_name,tls,tls.cert,tls.key" {
		t.Errorf("params = %v", names)
	}
	byName := map[string]ParamInfo{}
	for _, p := range role.Params {
		byName[p.Name] = p
	}
	if p := byName["server_name"]; !p.Required || p.Description != "The site's name. nginx serves it." || p.Pattern != "^[a-z.]+$" {
		t.Errorf("server_name = %+v", p)
	}
	if p := byName["listen_port"]; p.Default != float64(80) || p.Max == nil || *p.Max != 65535 {
		t.Errorf("listen_port = %+v", p)
	}
	if p := byName["tls.key"]; !p.Secret || p.Default != nil {
		t.Errorf("a secret's default is shown: %+v", p)
	}
	if len(role.Rules) != 1 || role.Rules[0].Message != "port 22 is SSH's" {
		t.Errorf("rules = %+v", role.Rules)
	}
	var ps []ProviderInfo
	if code := get(t, hs.URL+"/api/v1/providers", &ps); code != 200 || len(ps) == 0 || ps[0].Type == "" {
		t.Errorf("providers: %d %+v", code, ps)
	}
}
