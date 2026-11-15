package migrate

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/Warewave-Technology/gaia/internal/evidence"
	"github.com/Warewave-Technology/gaia/internal/verify"
	"github.com/Warewave-Technology/gaia/internal/vsphereread"
)

// Against vcsim: a powered-off VM goes through inventory, layout and download (all
// read-only) and then fails at the conversion (vcsim's disks are not real images, or
// virt-v2v is not installed); a powered-on VM and a missing VM are not checked. Every
// VM still gets a sealed bundle in the ledger, and no call was blocked.
func TestRunAgainstSimulator(t *testing.T) {
	m := simulator.ESX()
	if err := m.Create(); err != nil {
		t.Fatal(err)
	}
	defer m.Remove()
	s := m.Service.NewServer()
	defer s.Close()
	ctx := context.Background()

	plain, err := govmomi.NewClient(ctx, s.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.jsonl")
	src, err := connect(t, s.URL, callLog)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := src.Inventory(ctx)
	if err != nil || len(inv) < 2 {
		t.Fatalf("inventory %v %v", inv, err)
	}
	off, on := inv[0].Name, inv[1].Name
	vm := object.NewVirtualMachine(plain.Client, types.ManagedObjectReference{Type: "VirtualMachine", Value: inv[0].ObjectRef})
	task, err := vm.PowerOff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task.Wait(ctx)

	evidence.GenerateKey(filepath.Join(dir, "k.pem"), filepath.Join(dir, "p.pem"))
	key, _ := evidence.LoadPrivateKey(filepath.Join(dir, "k.pem"))
	pub, _ := evidence.LoadPublicKey(filepath.Join(dir, "p.pem"))
	var progress bytes.Buffer
	rep, err := Run(ctx, Options{Source: src, VMs: []string{off, on, "nope"}, WorkDir: filepath.Join(dir, "work"),
		ImageDir: filepath.Join(dir, "images"), CallLog: callLog, Log: &progress,
		Verify: verify.Options{Key: key, EvidenceDir: filepath.Join(dir, "ev"), Ledger: filepath.Join(dir, "ledger.jsonl"),
			WorkDir: dir, SerialDir: dir}})
	if err != nil {
		t.Fatalf("%v\n%s", err, progress.String())
	}
	t.Log(progress.String())
	if len(rep.VMs) != 3 || rep.Blocked != 0 || rep.Calls == 0 {
		t.Fatalf("report %+v", rep)
	}
	byName := map[string]Readiness{}
	for _, r := range rep.VMs {
		byName[r.VM] = r
	}
	step := func(vm, name string) verify.CheckResult {
		for _, c := range byName[vm].Checks {
			if c.Name == name {
				return c
			}
		}
		t.Fatalf("%s: no step %s in %+v", vm, name, byName[vm].Checks)
		return verify.CheckResult{}
	}
	// No virt-v2v here (or vcsim's disks are not images): the conversion is not OK, so neither is readiness.
	if c := step(off, "conversion"); step(off, "cold").Verdict != verify.OK || c.Verdict == verify.OK || byName[off].Readiness == verify.OK {
		t.Errorf("powered-off VM: %+v", byName[off])
	}
	if v := step(off, "download").Verdict; v != verify.OK && v != verify.Unknown {
		t.Errorf("download %s", v)
	}
	if step(on, "cold").Verdict != verify.Unknown || byName[on].Readiness != verify.Unknown {
		t.Errorf("powered-on VM: %+v", byName[on])
	}
	if step("nope", "source_vm").Verdict != verify.Unknown {
		t.Errorf("missing VM: %+v", byName["nope"])
	}
	lr, err := evidence.VerifyLedger(filepath.Join(dir, "ledger.jsonl"), filepath.Join(dir, "ev"), pub)
	if err != nil || !lr.Intact || lr.Entries != 3 {
		t.Fatalf("ledger %+v %v", lr, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "ev", byName[off].BundleID, "migration", "calls.jsonl"))
	if !strings.Contains(string(b), `"op":"RetrievePropertiesEx"`) || strings.Contains(string(b), `"allowed":false`) {
		t.Fatalf("bundle call log:\n%s", b)
	}
}

func connect(t *testing.T, u *url.URL, callLog string) (*vsphereread.Client, error) {
	t.Helper()
	pw, _ := u.User.Password()
	bare := *u
	bare.User = nil
	f, err := os.Create(callLog)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { f.Close() })
	return vsphereread.Connect(context.Background(), bare.String(), u.User.Username(), pw, true, f)
}

func TestToMiB(t *testing.T) {
	if toMiB(1048576, "KiB") != 1024 || toMiB(2, "GiB") != 2048 || toMiB(512, "MiB") != 512 || toMiB(2097152, "") != 2048 {
		t.Fatal("toMiB")
	}
}
