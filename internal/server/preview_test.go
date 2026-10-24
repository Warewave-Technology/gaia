package server

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

// pushBranch commits an edit of path to a new branch of a bare repository
// and pushes it to dst (a branch or another ref).
func pushBranch(t *testing.T, bare, dst, path string, edit func(string) string) {
	t.Helper()
	dir := t.TempDir()
	cl, err := git.PlainClone(dir, false, &git.CloneOptions{URL: bare})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, filepath.FromSlash(path))
	data, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(edit(string(data))), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := cl.Worktree()
	if _, err := wt.Add(path); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("edit", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@x", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Push(&git.PushOptions{RefSpecs: []config.RefSpec{config.RefSpec("refs/heads/master:" + dst)}}); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewPlansARefWithoutOpeningAChange(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	bare := t.TempDir()
	if _, err := git.PlainClone(bare, true, &git.CloneOptions{URL: e.repo}); err != nil {
		t.Fatal(err)
	}
	pushBranch(t, bare, "refs/heads/feature", "contracts/poc-web.yaml", func(s string) string { return strings.Replace(s, "replicas: 0", "replicas: 2", 1) })
	pushBranch(t, bare, "refs/pull/7/head", "contracts/poc-web.yaml", func(s string) string { return strings.Replace(s, "kind: Server", "kind: Sever", 1) })
	platform := repo.Rev{Name: "platform", URL: "file://" + bare, Ref: "master"}
	cat := &CatalogSource{Repos: e.repos, Platform: platform}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	// What is applied now is the followed branch.
	p, err := plan.Build(ctx, cat.Graph(), []string{"poc-web"}, apply.StoreState{S: e.store})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Q().SetApplied(ctx, db.SetAppliedParams{Contract: "poc-web", AppliedPlanHash: pgtype.Text{String: p.Hash, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	a := auth.New(e.store, cat.Graph, nil)
	srv, err := New(Config{Listen: "127.0.0.1:0", Auth: a, Catalog: cat, Repos: e.repos}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	admin := e.person(a, "ayse", "team-platform")
	viewer := e.person(a, "can", "team-web")
	get := func(tok, ref string, out any) int {
		return call(t, "GET", hs.URL+"/api/v1/preview?"+url.Values{"ref": {ref}}.Encode(), tok, out)
	}

	var pv Preview
	if code := get(admin, "master", &pv); code != 200 || len(pv.Contracts) != 1 || pv.Contracts[0].Changed {
		t.Fatalf("the followed branch: %d %+v", code, pv)
	}
	pv = Preview{}
	if code := get(admin, "feature", &pv); code != 200 || len(pv.Contracts) != 1 || !pv.Contracts[0].Changed || pv.Contracts[0].Plan == nil || len(pv.Contracts[0].Plan.Servers) != 2 {
		t.Fatalf("feature: %d %+v", code, pv)
	}
	pv = Preview{}
	if get(viewer, "feature", &pv); len(pv.Contracts) != 1 || !pv.Contracts[0].Restricted || pv.Contracts[0].Plan != nil || !pv.Contracts[0].Changed {
		t.Errorf("feature for someone else: %+v", pv)
	}
	pv = Preview{}
	if code := get(admin, "pull/7", &pv); code != 200 || !strings.Contains(pv.Error, "Sever") {
		t.Errorf("a pull request that does not load: %d %+v", code, pv)
	}
	if code := get(admin, "no-such-branch", nil); code != 422 {
		t.Errorf("an unknown branch: %d", code)
	}
	if code := get(admin, "a..b", nil); code != 422 {
		t.Errorf("not a ref: %d", code)
	}
	if code := call(t, "GET", hs.URL+"/api/v1/preview?ref=main&repo=nowhere", admin, nil); code != 404 {
		t.Errorf("an unknown repository: %d", code)
	}
	if _, ok := e.open("poc-web"); ok {
		t.Error("a preview opened a change")
	}
	// The followed branch is still the followed one.
	g, _ := git.PlainOpen(e.repos.Path(platform.Repo()))
	if h, err := g.Reference(plumbing.NewBranchReferenceName("master"), true); err != nil || h.Hash() == plumbing.ZeroHash {
		t.Errorf("mirror master: %v %v", h, err)
	}
}
