package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/crypto/ssh"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/change"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/signature"
)

func TestARevertIsAChangeTheRequesterMayNotApprove(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	mehmet := e.signer("mehmet", "team-platform")
	e.signer("ayse", "team-platform")
	e.signer("can", "team-web")
	e.edit("platform/policy.yaml", "test: { approval: none }", "test: { approval: required }")
	e.git.CommitSigned("approval", mehmet.git)
	e.edit("contracts/poc-web.yaml", "app: poc", "app: poc2")
	target := e.git.CommitSigned("rename app", mehmet.git)

	// The repository as a server would have it: bare, followed by URL.
	bare := t.TempDir()
	if _, err := git.PlainClone(bare, true, &git.CloneOptions{URL: e.repo}); err != nil {
		t.Fatal(err)
	}
	platform := repo.Rev{Name: "platform", URL: "file://" + bare, Ref: "master"}

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewSignerFromKey(priv)
	if err := change.RegisterGaiaKey(ctx, e.store, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := change.RegisterGaiaKey(ctx, e.store, key.PublicKey()); err != nil {
		t.Errorf("registering twice: %v", err)
	}
	cat := &CatalogSource{Repos: e.repos, Platform: platform}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	a := auth.New(e.store, cat.Graph, nil)
	w := &repo.Writer{Mirrors: e.repos, Signer: signature.SSHSigner{Key: key}, Name: "Gaia", Email: "gaia@example.com"}
	srv, err := New(Config{Listen: "127.0.0.1:0", Auth: a, Changes: en, Catalog: cat, Writer: w}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	evaluate := func() {
		t.Helper()
		snap, err := e.repos.Head(ctx, platform)
		if err != nil {
			t.Fatal(err)
		}
		g, err := e.repos.Graph(ctx, snap)
		if err != nil {
			t.Fatal(err)
		}
		if err := en.Evaluate(ctx, snap, g); err != nil {
			t.Fatal(err)
		}
	}
	evaluate()
	before, _ := e.open("poc-web")

	url := hs.URL + "/api/v1/commits/" + target + "/revert"
	var out map[string]string
	if code := send(t, "POST", hs.URL+"/api/v1/commits/abc/revert", e.token(a, "ayse", false), map[string]string{"repo": "platform"}, &out); code != 400 {
		t.Errorf("a short SHA: %d", code)
	}
	if code := send(t, "POST", url, e.token(a, "can", false), map[string]string{"repo": "platform"}, &out); code != 403 {
		t.Errorf("someone else's contract: %d %v", code, out)
	}
	if code := send(t, "POST", url, e.token(a, "ayse", false), map[string]string{"repo": "nowhere"}, &out); code != 404 {
		t.Errorf("an unknown repository: %d %v", code, out)
	}
	if code := send(t, "POST", url, e.token(a, "ayse", false), map[string]string{"repo": "platform"}, &out); code != 201 {
		t.Fatalf("revert: %d %v", code, out)
	}
	// The commit is on the branch, signed by Gaia.
	g, _ := git.PlainOpen(bare)
	head, _ := g.Head()
	if head.Hash().String() != out["commit"] {
		t.Fatalf("head %s, revert %s", head.Hash(), out["commit"])
	}

	evaluate()
	c, ok := e.open("poc-web")
	if !ok || c.ID == before.ID {
		t.Fatalf("no change for the revert: %+v", c)
	}
	var commits []change.Commit
	_ = json.Unmarshal(c.Commits, &commits)
	if strings.Join(c.RequestedBy, ",") != "ayse" || len(commits) != 1 || commits[0].Signer != "gaia" || commits[0].For != "ayse" || commits[0].Problem != "" || c.Source != "revert" {
		t.Fatalf("change by %v, commits %+v", c.RequestedBy, commits)
	}
	if g := gates(t, e, c.ID); g["signature"] != "ok" || g["approval"] != "waiting" {
		t.Errorf("gates = %v", g)
	}
	// ayse asked for it: mehmet approves, she may not.
	body := map[string]string{"planHash": c.PlanHash}
	if code := send(t, "POST", hs.URL+"/api/v1/changes/"+c.ID+"/approve", e.token(a, "ayse", true), body, &out); code != 403 {
		t.Errorf("the requester approves: %d %v", code, out)
	}
	if code := send(t, "POST", hs.URL+"/api/v1/changes/"+c.ID+"/approve", e.token(a, "mehmet", true), body, &out); code != 200 {
		t.Errorf("mehmet approves: %d %v", code, out)
	}

	// Someone pushes a commit that changes the same file: reverting the
	// target again is refused, naming the file.
	dir := t.TempDir()
	cl, err := git.PlainClone(dir, false, &git.CloneOptions{URL: bare})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "contracts", "poc-web.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "contracts", "poc-web.yaml"), []byte(strings.Replace(string(data), "labels: {", "labels: { tier: web,", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := cl.Worktree()
	if _, err := wt.Add("contracts/poc-web.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("again", &git.CommitOptions{Author: &object.Signature{Name: "m", Email: "m@x", When: time.Now()}, Signer: mehmet.git}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if code := send(t, "POST", url, e.token(a, "ayse", false), map[string]string{"repo": "platform"}, &out); code != 409 || !strings.Contains(out["error"], "contracts/poc-web.yaml") {
		t.Errorf("a conflicting revert: %d %v", code, out)
	}
}
