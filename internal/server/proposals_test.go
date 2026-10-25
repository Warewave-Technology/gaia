package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/auth"
	"github.com/Warewave-Technology/gaia/internal/proposal"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/settings"
	"github.com/Warewave-Technology/gaia/internal/store/db"
)

const sizeYAML = "apiVersion: gaia/v1alpha1\nkind: Size\nmetadata: { name: %s }\nspec: { cpu: %s, memoryGiB: 2, diskGiB: 20 }\n"

func sizeEdit(name, cpu string) proposal.Edit {
	return proposal.Edit{YAML: strings.NewReplacer("%s", name).Replace(strings.Replace(sizeYAML, "cpu: %s", "cpu: "+cpu, 1))}
}

func TestSettingsProposals(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.edit("contracts/poc-web.yaml", "replicas: 0", "replicas: 1")
	e.git.Commit("one server")
	e.signer("mehmet", "team-ops")      // operator
	e.signer("ayse", "team-platform")   // platform admin
	e.signer("zeynep", "team-platform") // platform admin
	e.signer("can", "team-web")         // viewer
	sets := &settings.Store{DB: e.store}
	e.repos.Settings = sets
	platform := repo.Rev{Name: "platform", URL: e.repo}
	if _, err := settings.ImportGit(ctx, sets, e.repos, platform); err != nil {
		t.Fatal(err)
	}
	cat := &CatalogSource{Repos: e.repos, Platform: platform}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	a := auth.New(e.store, cat.Graph, nil)
	props := &proposal.Service{Store: e.store, Settings: sets, Repos: e.repos, Desired: cat}
	srv, err := New(Config{Listen: "127.0.0.1:0", Auth: a, Catalog: cat, Settings: sets, Proposals: props}, e.store)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	api := hs.URL + "/api/v1/settings"
	mehmet, ayse, zeynep, can := e.token(a, "mehmet", true), e.token(a, "ayse", true), e.token(a, "zeynep", true), e.token(a, "can", true)
	edits := func(es ...proposal.Edit) map[string]any { return map[string]any{"changes": es} }

	// A check says what a change would do, and what is wrong with it.
	var c proposal.Check
	if code := send(t, "POST", api+"/check", mehmet, edits(sizeEdit("small", "3")), &c); code != 200 || len(c.Errors) != 0 || c.Base != 1 || len(c.Impact) != 1 || c.Impact[0].Contract != "poc-web" || c.Objects[0].Op != "change" {
		t.Fatalf("check: %d %+v", code, c)
	}
	if send(t, "POST", api+"/check", mehmet, edits(sizeEdit("small", "0")), &c); len(c.Errors) == 0 || !strings.Contains(strings.Join(c.Errors, " "), "at least 1") {
		t.Errorf("a size of no CPU: %+v", c.Errors)
	}
	if send(t, "POST", api+"/check", mehmet, edits(proposal.Edit{Kind: "Size", Name: "small", Delete: true}), &c); len(c.Errors) == 0 {
		t.Errorf("removing a size poc-web uses: %+v", c)
	}
	if send(t, "POST", api+"/check", mehmet, edits(proposal.Edit{YAML: "apiVersion: gaia/v1alpha1\nkind: Baseline\nmetadata: { name: b }\nspec: { os: linux, playbook: b.yml }\n"}), &c); !strings.Contains(strings.Join(c.Errors, " "), "it is changed in Git") {
		t.Errorf("a Git kind: %+v", c.Errors)
	}
	if code := send(t, "POST", api+"/check", can, edits(sizeEdit("small", "3")), nil); code != 403 {
		t.Errorf("a viewer checks: %d", code)
	}

	propose := func(tok, title string, es ...proposal.Edit) Proposal {
		t.Helper()
		var p Proposal
		if code := send(t, "POST", api+"/proposals", tok, map[string]any{"title": title, "changes": es}, &p); code != 201 {
			t.Fatalf("propose %s: %d %+v", title, code, p)
		}
		return p
	}
	decide := func(tok string, p Proposal, verb, comment string) (int, Proposal) {
		t.Helper()
		var out Proposal
		code := send(t, "POST", api+"/proposals/"+p.ID+"/"+verb, tok, map[string]string{"contentHash": p.ContentHash, "comment": comment}, &out)
		return code, out
	}

	if code := send(t, "POST", api+"/proposals", mehmet, map[string]any{"title": "x", "changes": []proposal.Edit{sizeEdit("small", "0")}}, nil); code != 422 {
		t.Errorf("an invalid proposal: %d", code)
	}
	small := propose(mehmet, "small: 3 vCPU", sizeEdit("small", "3"))
	if small.Status != "pending" || small.Critical || len(small.Impact) != 1 || small.MayDecide {
		t.Errorf("small = %+v", small)
	}
	var mine []Proposal
	if send(t, "GET", api+"/proposals?awaiting=me", ayse, nil, &mine); len(mine) != 1 || mine[0].ID != small.ID {
		t.Errorf("awaiting ayse: %+v", mine)
	}
	if send(t, "GET", api+"/proposals?awaiting=me", can, nil, &mine); len(mine) != 0 {
		t.Errorf("awaiting can: %+v", mine)
	}
	if code, _ := decide(mehmet, small, "approve", ""); code != 403 {
		t.Errorf("the proposer approves: %d", code)
	}
	if code, _ := decide(can, small, "approve", ""); code != 403 {
		t.Errorf("a viewer approves: %d", code)
	}
	if code, _ := decide(e.token(a, "ayse", false), small, "approve", ""); code != 403 {
		t.Errorf("without a step-up: %d", code)
	}
	wrong := small
	wrong.ContentHash = "sha256:other"
	if code, _ := decide(ayse, wrong, "approve", ""); code != 409 {
		t.Errorf("another content: %d", code)
	}
	code, out := decide(ayse, small, "approve", "ok")
	if code != 200 || out.Status != "applied" || out.AppliedVersion != 2 || out.DecidedBy != "ayse" {
		t.Fatalf("approve: %d %+v", code, out)
	}
	if v, _ := sets.Latest(ctx); v != 2 {
		t.Errorf("latest = %d", v)
	}
	var detail Proposal
	if send(t, "GET", api+"/proposals/"+small.ID, can, nil, &detail); len(detail.Objects) != 1 || !strings.Contains(detail.Objects[0].Before, "cpu: 2") || !strings.Contains(detail.Objects[0].After, "cpu: 3") {
		t.Errorf("detail = %+v", detail.Objects)
	}

	// The Policy is a platform admin's; then team-web approves the rest.
	policy := `apiVersion: gaia/v1alpha1
kind: Policy
metadata: { name: default }
spec:
  naming: site.name + env.substring(0, 1) + app.substring(0, 3) + pad(seq, 2)
  requiredLabels: [env, owner, app]
  environments:
    test: { approval: none }
  gaiaRoles:
    platform-admin: [team-platform]
    operator: [team-ops]
  settings: { approvers: [team-web] }
`
	pp := propose(ayse, "settings approvers", proposal.Edit{YAML: policy})
	if !pp.Critical || len(pp.WhyCritical) == 0 {
		t.Errorf("a Policy change = %+v", pp)
	}
	if code, _ := decide(mehmet, pp, "approve", ""); code != 403 {
		t.Errorf("an operator approves the Policy: %d", code)
	}
	if code, out := decide(zeynep, pp, "approve", ""); code != 200 || out.AppliedVersion != 3 {
		t.Fatalf("zeynep approves the Policy: %d %+v", code, out)
	}
	if err := cat.Load(ctx); err != nil {
		t.Fatal(err)
	}
	medium := propose(mehmet, "medium: 6 vCPU", sizeEdit("medium", "6"))
	if code, out := decide(can, medium, "approve", ""); code != 200 || out.Status != "applied" {
		t.Errorf("a settings approver: %d %+v", code, out)
	}

	// Two proposals for the same object: the first applied makes the
	// other stale. One for another object is applied on the newer version.
	first := propose(mehmet, "small: 4", sizeEdit("small", "4"))
	second := propose(zeynep, "small: 5", sizeEdit("small", "5"))
	other := propose(zeynep, "medium: 8", sizeEdit("medium", "8"))
	if code, _ := decide(ayse, first, "approve", ""); code != 200 {
		t.Fatalf("first: %d", code)
	}
	if code, _ := decide(ayse, second, "approve", ""); code != 409 {
		t.Errorf("a stale proposal: %d", code)
	}
	send(t, "GET", api+"/proposals/"+second.ID, ayse, nil, &detail)
	if detail.Status != "stale" || !strings.Contains(detail.StaleReason, "aynı nesneleri") {
		t.Errorf("second = %+v", detail)
	}
	if code, out := decide(ayse, other, "approve", ""); code != 200 || out.AppliedVersion != 6 {
		t.Errorf("another object on a newer version: %d %+v", code, out)
	}

	rej := propose(mehmet, "small: 1", sizeEdit("small", "1"))
	if code, _ := decide(ayse, rej, "reject", ""); code != 400 {
		t.Errorf("a rejection without a reason: %d", code)
	}
	if code, out := decide(ayse, rej, "reject", "too small"); code != 200 || out.Status != "rejected" || out.Comment != "too small" {
		t.Errorf("reject: %d %+v", code, out)
	}
	wd := propose(mehmet, "small: 2", sizeEdit("small", "2"))
	if code := send(t, "POST", api+"/proposals/"+wd.ID+"/withdraw", ayse, nil, nil); code != 403 {
		t.Errorf("someone else withdraws: %d", code)
	}
	if code := send(t, "POST", api+"/proposals/"+wd.ID+"/withdraw", mehmet, nil, &out); code != 200 || out.Status != "withdrawn" {
		t.Errorf("withdraw: %d %+v", code, out)
	}

	log, _ := e.store.Q().AuditLog(ctx, db.AuditLogParams{MaxRows: 100})
	actions := map[string]int{}
	for _, l := range log {
		actions[l.Action]++
	}
	if actions["settings.propose"] != 8 || actions["settings.approve"] != 5 || actions["settings.apply"] != 5 || actions["settings.reject"] != 1 || actions["settings.withdraw"] != 1 {
		t.Errorf("audit = %v", actions)
	}
}
