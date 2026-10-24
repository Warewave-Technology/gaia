package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/plan"
)

func TestPlan(t *testing.T) {
	r := labRepo(t)
	commit := r.Commit("first")

	out := run(t, NewRootCommand("dev"), "plan", "--repo", r.Dir)
	for _, want := range []string{
		"commit " + commit[:12],
		"Server/poc-web  env test  approval none",
		"+ labtpoc01",
		"provision  + 1 VM(s) (pve-lab)",
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
