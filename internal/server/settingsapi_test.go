package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/settings"
)

func TestSettingsAndAuditAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	sets := &settings.Store{DB: e.store}
	e.repos.Settings = sets
	platform := repo.Rev{Name: "platform", URL: e.repo, Paths: []string{"platform", "contracts"}}
	if v, err := settings.ImportGit(ctx, sets, e.repos, platform); err != nil || v != 1 {
		t.Fatalf("import: %d %v", v, err)
	}
	cat := &CatalogSource{Repos: e.repos, Platform: platform}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{Listen: "127.0.0.1:0", Catalog: cat, Settings: sets, Sources: &Sources{Catalog: cat, Store: e.store}}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	var state SettingsState
	if code := get(t, hs.URL+"/api/v1/settings", &state); code != 200 || !state.Store || state.Current == nil || state.Current.Version != 1 || state.Current.Author != "gaia" {
		t.Fatalf("settings: %d %+v", code, state)
	}
	// Git still has every setting it had; each is listed as left out.
	found := false
	for _, ig := range state.Ignored {
		found = found || (ig.Kind == "Network" && ig.Name == "lab-poc" && strings.HasPrefix(ig.Source, "platform/catalog/network.yaml"))
	}
	if !found {
		t.Errorf("ignored = %+v", state.Ignored)
	}
	var versions []SettingsVersion
	if get(t, hs.URL+"/api/v1/settings/versions", &versions); len(versions) != 1 || versions[0].Objects == 0 || !strings.Contains(versions[0].Reason, "Git'ten içeri alındı") {
		t.Errorf("versions = %+v", versions)
	}
	var v1 struct {
		SettingsVersion
		Files []SettingsFile
	}
	if code := get(t, hs.URL+"/api/v1/settings/versions/1", &v1); code != 200 || len(v1.Files) != v1.Objects || v1.Files[0].Kind == "" {
		t.Errorf("version 1: %d %+v", code, v1)
	}
	if code := get(t, hs.URL+"/api/v1/settings/versions/9", nil); code != 404 {
		t.Errorf("no version 9: %d", code)
	}
	var c struct{ Objects []CatalogObject }
	get(t, hs.URL+"/api/v1/catalog?kind=Network", &c)
	if len(c.Objects) != 1 || c.Objects[0].Source != "settings:Network/lab-poc.yaml:1" {
		t.Errorf("networks = %+v", c.Objects)
	}

	if code := post(t, hs.URL+"/api/v1/sources/sync"); code != 202 {
		t.Errorf("sync: %d", code)
	}
	var log []AuditEntry
	if get(t, hs.URL+"/api/v1/audit?action=sources.", &log); len(log) != 1 || log[0].Action != "sources.sync" || log[0].Address != "127.0.0.1" {
		t.Errorf("audit = %+v", log)
	}
	get(t, hs.URL+"/api/v1/audit", &log)
	if len(log) != 2 || log[1].Action != "settings.import" || !strings.Contains(string(log[1].Detail), `"version":1`) {
		t.Errorf("audit = %+v", log)
	}
	resp, err := http.Get(hs.URL + "/api/v1/audit?format=jsonl")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/x-ndjson" || strings.Count(string(body), "\n") != 2 {
		t.Errorf("jsonl: %s %q", resp.Header.Get("Content-Type"), body)
	}
	if code := get(t, hs.URL+"/api/v1/audit?since=yesterday", nil); code != 400 {
		t.Errorf("a bad since: %d", code)
	}
}
