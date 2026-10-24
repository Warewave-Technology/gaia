package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/itest"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func TestPlan(t *testing.T) {
	r := labRepo(t)
	commit := r.Commit("first")

	out := run(t, NewRootCommand("dev"), "plan", "--repo", r.Dir)
	for _, want := range []string{
		"commit " + commit[:12],
		"Server/poc-web  env test  approval none",
		"+ labtpoc01",
		"provision  +1 =0 (pve-lab)",
		"plan sha256:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestPlanJSONIsOnlyThePlan(t *testing.T) {
	r := labRepo(t)
	commit := r.Commit("first")

	out := run(t, NewRootCommand("dev"), "plan", "--repo", r.Dir, "--json", "poc-web")
	var p plan.Plan
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if p.Commit != commit || len(p.Contracts) != 1 || p.Contracts[0].Servers[0].Hostname != "labtpoc01" || !strings.HasPrefix(p.Hash, "sha256:") {
		t.Errorf("plan = %+v", p)
	}
}

func TestPlanUnknownContract(t *testing.T) {
	r := labRepo(t)
	r.Commit("first")
	root := NewRootCommand("dev")
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"plan", "--repo", r.Dir, "nope"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), `no Server contract "nope"`) {
		t.Errorf("err = %v", err)
	}
}

func TestPlanReadsExistingServers(t *testing.T) {
	itest.Require(t)
	dsn := itest.StartPostgres(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateHost(ctx, store.NewHost{UID: "uidexisting00001", Contract: "poc-web", Hostname: "labtpoc01", Site: "lab", Network: "lab-poc", Seq: 1}); err != nil {
		t.Fatal(err)
	}

	r := labRepo(t)
	r.Commit("first")
	out := run(t, NewRootCommand("dev"), "plan", "--repo", r.Dir, "--dsn", dsn)
	for _, want := range []string{"= labtpoc01", "uid uidexisting00001 (provisioning)", "no changes to servers"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	t.Setenv("GAIA_DSN", "")
	if out := run(t, NewRootCommand("dev"), "plan", "--repo", r.Dir); !strings.Contains(out, "every server is planned as new") || !strings.Contains(out, "+ labtpoc01") {
		t.Errorf("without a database:\n%s", out)
	}
}
