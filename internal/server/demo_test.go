package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

// plainTransit stands in for OpenBao's transit engine in the demo.
type plainTransit struct{}

func (plainTransit) Encrypt(_ context.Context, _ string, b []byte) (string, error) {
	return "demo:" + base64.StdEncoding.EncodeToString(b), nil
}

func (plainTransit) Decrypt(_ context.Context, _ string, s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s[len("demo:"):])
}

// TestUIDemo runs a whole server for looking at the UI against a real
// API: the lab catalog in a test repository, a change that waits for
// approval, sign-in with password and TOTP, and runs on fake systems.
// It runs only with GAIA_UI_DEMO set to how long to serve:
//
//	make web && GAIA_INTEGRATION=1 GAIA_UI_DEMO=30m go test ./internal/server -run TestUIDemo -v -timeout 0
func TestUIDemo(t *testing.T) {
	d, err := time.ParseDuration(os.Getenv("GAIA_UI_DEMO"))
	if err != nil {
		t.Skip("set GAIA_UI_DEMO to a duration to serve the UI demo")
	}
	// The UI is opened at localhost: WebAuthn takes no IP address.
	port := os.Getenv("GAIA_UI_DEMO_PORT")
	if port == "" {
		port = "8090"
	}
	addr, origin := "127.0.0.1:"+port, "localhost:"+port
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	mehmet := e.signer("mehmet", "team-platform")
	e.signer("ayse", "team-platform")
	e.edit("platform/policy.yaml", "test: { approval: none }", "test: { approval: required }")
	e.edit("platform/auth.yaml", "url: http://localhost:8080", "url: http://"+origin)
	e.edit("platform/auth.yaml", "require: passkey", "require: mfa")
	e.git.CommitSigned("demo: test needs approval, sign in with password and TOTP", mehmet.git)

	cat := &CatalogSource{Repos: e.repos, Platform: repo.Rev{Name: "platform", URL: e.repo}}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	a := auth.New(e.store, cat.Graph, nil).WithTransit(plainTransit{})
	srv, err := New(Config{Listen: addr, Workers: 1, Repos: e.repos, Systems: e.systems, Finished: e.finished, StopTimeout: 5 * time.Second,
		Auth: a, Changes: en, Catalog: cat}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()

	e.evaluate(en)
	u, err := e.store.Q().UserByName(ctx, "ayse")
	if err != nil {
		t.Fatal(err)
	}
	link, err := a.Enroll(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("\nGaia UI demo on http://%s for %s\nayse (team-platform) enrolls here: %s\n\n", origin, d, link)
	// Changes are re-evaluated as the repository moves.
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		case <-tick.C:
			e.evaluate(en)
		}
	}
}
