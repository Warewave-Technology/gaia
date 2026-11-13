package vsphereprobe

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
)

// TestAgainstSimulator runs the whole probe against vcsim. vcsim does not
// implement every call (e.g. QueryChangedDiskAreas), so this test only pins
// what must always work: the throwaway VM is created, a snapshot is taken,
// cleanup runs, and the report always carries a verdict.
func TestAgainstSimulator(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		r := Run(ctx, c, Config{ReadBytes: 1 << 20})
		for _, s := range r.Steps {
			t.Logf("%-20s %-4s %s", s.ID, s.Status, s.Detail)
		}
		t.Logf("verdict %s: %s", r.Verdict, r.Summary)

		status := map[string]Status{}
		for _, s := range r.Steps {
			status[s.ID] = s.Status
		}
		for _, id := range []string{stepVM, stepSnapshot, "snapshot-remove", "vm-destroy"} {
			if status[id] != Pass {
				t.Errorf("step %s = %q, want PASS", id, status[id])
			}
		}
		if r.Verdict == "" || r.Summary == "" {
			t.Error("report has no verdict")
		}
	})
}

func TestVerdictRules(t *testing.T) {
	cases := []struct {
		name  string
		steps map[string]Status
		want  Verdict
	}{
		{"no vm", map[string]Status{stepVM: Fail}, Unknown},
		{"snapshot refused", map[string]Status{stepVM: Pass, stepSnapshot: Fail, stepNFC: Pass}, Critical},
		{"no reads", map[string]Status{stepVM: Pass, stepSnapshot: Pass, stepNFC: Fail, stepHTTP: Fail}, Critical},
		{"all good", map[string]Status{stepVM: Pass, stepCBT: Pass, stepSnapshot: Pass, stepQuery: Pass, stepHTTP: Pass}, OK},
		{"no cbt", map[string]Status{stepVM: Pass, stepCBT: Fail, stepSnapshot: Pass, stepNFC: Pass}, Warning},
	}
	for _, tc := range cases {
		p := &run{r: &Report{}}
		for id, st := range tc.steps {
			p.r.Steps = append(p.r.Steps, Step{ID: id, Status: st})
		}
		p.finish()
		if p.r.Verdict != tc.want {
			t.Errorf("%s: verdict %s, want %s", tc.name, p.r.Verdict, tc.want)
		}
	}
}
