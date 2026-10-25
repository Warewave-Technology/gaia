package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/Warewave-Technology/gaia/internal/provider/check"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

func TestProviderChecks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	cat := &CatalogSource{Repos: e.repos, Platform: repo.Rev{Name: "platform", URL: e.repo}}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	old := check.Timeout
	check.Timeout = 2 * time.Second
	defer func() { check.Timeout = old }()
	w := &queue.CheckProvidersWorker{Store: e.store, Graph: cat.Graph, Systems: e.systems}
	if err := w.Work(ctx, &river.Job[queue.CheckProvidersArgs]{}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{Listen: "127.0.0.1:0", Catalog: cat}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	var ps []ProviderInfo
	if code := get(t, hs.URL+"/api/v1/providers", &ps); code != 200 || len(ps) == 0 {
		t.Fatalf("providers: %d %+v", code, ps)
	}
	// The lab's systems are not here: every provider is checked and
	// fails, but for those that have no harmless call.
	for _, p := range ps {
		switch {
		case p.Check == nil:
			t.Errorf("%s: not checked", p.Name)
		case p.Type == "git" && p.Check.Status != check.Skipped:
			t.Errorf("%s: %+v", p.Name, p.Check)
		case p.Type != "git" && p.Check.Status != check.Failed || p.Check.Status == check.Failed && p.Check.Error == "" || p.Check.LastOKAt != nil:
			t.Errorf("%s (%s): %+v", p.Name, p.Type, p.Check)
		}
	}
	if code := post(t, hs.URL+"/api/v1/providers/check"); code != 202 {
		t.Errorf("check now: %d", code)
	}
}
