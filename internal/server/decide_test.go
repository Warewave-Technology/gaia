package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/change"
	"github.com/Warewave-Technology/gaia/internal/repo"
)

func send(t *testing.T, method, url, token string, body, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, url, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// token signs someone in; strong as if they just confirmed with a passkey.
func (e *env) token(a *auth.Service, name string, strong bool) string {
	e.t.Helper()
	u, err := e.store.Q().UserByName(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	tok, err := a.IssueAccessToken(context.Background(), u.ID, time.Hour, strong)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func TestApproveAndRejectThroughTheAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	e.finished = en.Finished
	mehmet := e.signer("mehmet", "team-platform")
	e.signer("ayse", "team-platform")
	e.signer("can", "team-web")
	e.edit("platform/policy.yaml", "test: { approval: none }", "test: { approval: required }")
	e.git.CommitSigned("test needs approval", mehmet.git)

	cat := &CatalogSource{Repos: e.repos, Platform: repo.Rev{Name: "platform", URL: e.repo}}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	a := auth.New(e.store, cat.Graph, nil)
	srv, err := New(Config{Listen: "127.0.0.1:0", Workers: 1, Repos: e.repos, Systems: e.systems, Finished: e.finished, StopTimeout: 5 * time.Second, Auth: a, Changes: en}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	e.evaluate(en)
	c, ok := e.open("poc-web")
	if !ok {
		t.Fatal("no change")
	}
	url := hs.URL + "/api/v1/changes/" + c.ID
	body := map[string]string{"planHash": c.PlanHash}
	var out map[string]any
	if code := send(t, "POST", url+"/approve", e.token(a, "ayse", false), body, &out); code != 403 || out["stepUp"] != true {
		t.Errorf("without a recent passkey: %d %v", code, out)
	}
	ayse := e.token(a, "ayse", true)
	if code := send(t, "POST", url+"/approve", ayse, map[string]string{"planHash": "sha256:another"}, &out); code != 409 {
		t.Errorf("another plan: %d %v", code, out)
	}
	if code := send(t, "POST", url+"/approve", e.token(a, "mehmet", true), body, &out); code != 403 {
		t.Errorf("the signer approves: %d %v", code, out)
	}
	if code := send(t, "POST", url+"/approve", e.token(a, "can", true), body, &out); code != 403 {
		t.Errorf("someone who sees the name only: %d %v", code, out)
	}
	if code := send(t, "POST", url+"/reject", ayse, body, &out); code != 400 {
		t.Errorf("a rejection without a reason: %d %v", code, out)
	}
	if code := send(t, "POST", url+"/approve", ayse, body, &out); code != 200 {
		t.Fatalf("approve: %d %v", code, out)
	}
	if code := send(t, "POST", url+"/approve", ayse, body, &out); code != 409 {
		t.Errorf("approve twice: %d %v", code, out)
	}
	c = e.waitChange(c.ID, change.Applied, change.Failed)
	if c.Status != change.Applied {
		t.Fatalf("change = %+v", c)
	}
	rec, err := e.store.Q().RunRecord(ctx, c.RunID.String)
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ ApprovedBy []string }
	_ = json.Unmarshal([]byte(rec.Body), &r)
	if len(r.ApprovedBy) != 1 || r.ApprovedBy[0] != "ayse" {
		t.Errorf("record approvedBy = %v", r.ApprovedBy)
	}

	// A rejection closes the change, and the same plan is not offered
	// again until Git changes.
	e.edit("contracts/poc-web.yaml", "app: poc", "app: poc2")
	e.git.CommitSigned("rename app", mehmet.git)
	e.evaluate(en)
	c2, _ := e.open("poc-web")
	url = hs.URL + "/api/v1/changes/" + c2.ID
	if code := send(t, "POST", url+"/reject", ayse, map[string]string{"planHash": c2.PlanHash, "comment": "not now"}, &out); code != 200 || out["status"] != change.Rejected {
		t.Fatalf("reject: %d %v", code, out)
	}
	e.evaluate(en)
	if _, ok := e.open("poc-web"); ok {
		t.Error("a rejected change came back")
	}
	if code := send(t, "POST", url+"/approve", ayse, map[string]string{"planHash": c2.PlanHash}, &out); code != 409 {
		t.Errorf("approve a rejected change: %d", code)
	}
	approvals, _ := e.store.Q().Approvals(ctx, c2.ID)
	if len(approvals) != 1 || approvals[0].Decision != "reject" || approvals[0].Comment.String != "not now" {
		t.Errorf("approvals = %+v", approvals)
	}
}

// One commit that changes two owners' contracts: a platform-admin may
// approve both, at once.
func TestAPlatformAdminApprovesACommitAcrossOwners(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	en := e.engine()
	signer := e.signer("mehmet")
	e.signer("ayse", "team-platform") // platform-admin, and owner of poc-web
	e.signer("can", "team-web")       // owner of poc-api
	e.git.Write("contracts/poc-api.yaml", `apiVersion: gaia/v1alpha1
kind: Server
metadata:
  name: poc-api
  labels: { app: api, env: test, owner: team-web }
spec:
  replicas: 0
  image: rocky9
  size: small
  network: lab-poc
  baseline: linux-base
`)
	e.edit("platform/policy.yaml", "test: { approval: none }", "test: { approval: required }")
	head := e.git.CommitSigned("two contracts", signer.git)

	cat := &CatalogSource{Repos: e.repos, Platform: repo.Rev{Name: "platform", URL: e.repo}}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	a := auth.New(e.store, cat.Graph, nil)
	srv, err := New(Config{Listen: "127.0.0.1:0", Auth: a, Changes: en}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	e.evaluate(en)
	e.evaluate(en) // the first contract learns of the second's change
	ayse := e.token(a, "ayse", true)
	var listed []Change
	if call(t, "GET", hs.URL+"/api/v1/changes?open=true&commit="+head, ayse, &listed); len(listed) != 2 {
		t.Fatalf("the commit's changes = %+v", listed)
	}
	var body struct {
		Changes []map[string]string `json:"changes"`
	}
	for _, c := range listed {
		body.Changes = append(body.Changes, map[string]string{"id": c.ID, "planHash": c.PlanHash})
	}
	// ayse approves both: poc-web as its owner, poc-api (team-web's) as a
	// platform-admin, since the same commit changes both owners' contracts.
	var batch []map[string]string
	if code := send(t, "POST", hs.URL+"/api/v1/changes/approve", ayse, body, &batch); code != 200 || len(batch) != 2 {
		t.Fatalf("ayse's batch: %d %v", code, batch)
	}
	for _, r := range batch {
		if r["error"] != "" {
			t.Errorf("ayse's batch = %v", batch)
		}
	}
	for _, c := range listed {
		if g := gates(t, e, c.ID); g["approval"] != "ok" {
			t.Errorf("%s gates = %v", c.Contract, g)
		}
	}
	// can sees poc-web by name only, and poc-api is decided already.
	var late []map[string]string
	if code := send(t, "POST", hs.URL+"/api/v1/changes/approve", e.token(a, "can", true), body, &late); code != 200 || len(late) != 2 || late[0]["error"] == "" || late[1]["error"] == "" {
		t.Errorf("can's batch: %d %v", code, late)
	}
}
