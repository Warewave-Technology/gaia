package vsphereprobe

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// TestAgainstSimulator runs the whole probe against vcsim. vcsim does not
// implement every call (e.g. QueryChangedDiskAreas), so this test only pins
// what must always work: the throwaway VM is created, a snapshot is taken,
// cleanup runs, and the report always carries a verdict.
//
// The model is a standalone ESXi host, which is what the probe targets (free
// ESXi). vcsim's default vCenter model has several resource pools, so the
// probe's default-pool lookup is ambiguous there by design.
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
	}, simulator.ESX())
}

// TestExistingVM covers -vm: the VM is looked up in the default datacenter.
func TestExistingVM(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		f := find.NewFinder(c, true)
		dc, err := f.DefaultDatacenter(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.SetDatacenter(dc)
		vms, err := f.VirtualMachineList(ctx, "*")
		if err != nil {
			t.Fatal(err)
		}
		// Give the disk real data: a ranged GET of a non-empty file is answered with
		// 206 Partial Content, as real ESXi does (T2), and the read step must accept it.
		disk, err := firstDisk(ctx, vms[0])
		if err != nil {
			t.Fatal(err)
		}
		var path object.DatastorePath
		path.FromString(disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo).FileName)
		ds, err := f.Datastore(ctx, path.Datastore)
		if err != nil {
			t.Fatal(err)
		}
		flat := strings.TrimSuffix(path.Path, ".vmdk") + "-flat.vmdk"
		data := bytes.Repeat([]byte{0xa5}, 2<<20)
		if err := ds.Upload(ctx, bytes.NewReader(data), flat, &soap.DefaultUpload); err != nil {
			t.Fatal(err)
		}

		r := Run(ctx, c, Config{VMName: vms[0].Name(), ReadBytes: 1 << 20})
		for _, s := range r.Steps {
			t.Logf("%-20s %-4s %s", s.ID, s.Status, s.Detail)
			if s.ID == stepVM && s.Status != Pass {
				t.Errorf("step vm = %q (%s), want PASS", s.Status, s.Detail)
			}
			if s.ID == stepHTTP && s.Status != Pass {
				t.Errorf("step %s = %q (%s), want PASS", stepHTTP, s.Status, s.Detail)
			}
			if s.ID == "vm-destroy" {
				t.Error("an existing VM must never be destroyed")
			}
		}
	}, simulator.ESX())
}

func TestExitCode(t *testing.T) {
	for v, want := range map[Verdict]int{OK: 0, Warning: 1, Critical: 2, Unknown: 3, "": 3, "bogus": 3} {
		if got := v.ExitCode(); got != want {
			t.Errorf("%q.ExitCode() = %d, want %d", v, got, want)
		}
	}
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
