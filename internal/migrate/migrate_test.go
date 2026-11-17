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
	if err != nil || !lr.Intact || lr.Entries != 4 { // the genesis key entry and three bundles
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

func TestDefaultTemplate(t *testing.T) {
	for id, want := range map[string]string{
		"windows2019srvNext_64Guest": "windows-basic", "windows9Server64Guest": "windows-basic",
		"debian12_64Guest": "linux-basic", "": "linux-basic",
	} {
		if got := defaultTemplate(id); got != want {
			t.Errorf("defaultTemplate(%q) = %s, want %s", id, got, want)
		}
	}
}

func TestErrorLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v2v.log")
	os.WriteFile(p, []byte("[   7.7] Converting Windows Server 2022\nvirt-v2v: error: One of rhsrvany.exe or pvvxsvc.exe is missing\n\nIf reporting bugs, run virt-v2v with debugging enabled:\n  virt-v2v -v -x [...]\n"), 0o644)
	if got := errorLine(p); got != "virt-v2v: error: One of rhsrvany.exe or pvvxsvc.exe is missing" {
		t.Fatalf("errorLine = %q", got)
	}
	os.WriteFile(p, []byte("killed\n"), 0o644)
	if got := errorLine(p); got != "last line: killed" {
		t.Fatalf("errorLine = %q", got)
	}
}

// fakeConverter writes what a conversion would: a disk and a domain XML with the given OS and firmware.
type fakeConverter struct {
	state, os, firmware string
	virtioWin           bool
}

func (f fakeConverter) Convert(_ context.Context, in ConvertInput) ConvertOutput {
	out := ConvertOutput{State: f.state, Detail: "fake " + f.state, Seconds: 1, VirtioWin: f.virtioWin}
	if f.state != "converted" {
		return out
	}
	disk, dom := filepath.Join(in.ImageDir, in.Name+"-sda"), filepath.Join(in.ImageDir, in.Name+".xml")
	os.WriteFile(disk, []byte("qcow2"), 0o644)
	fw := ""
	if f.firmware == "efi" {
		fw = " firmware='efi'"
	}
	os.WriteFile(dom, []byte(`<domain type='kvm'><name>`+in.Name+`</name><metadata><libosinfo:libosinfo xmlns:libosinfo="http://libosinfo.org/xmlns/libvirt/domain/1.0">`+
		`<libosinfo:os id="`+f.os+`"/></libosinfo:libosinfo></metadata><memory unit='KiB'>2097152</memory><vcpu>2</vcpu>`+
		`<os`+fw+`><type arch='x86_64' machine='q35'>hvm</type></os><devices><disk device='disk'><target dev='vda' bus='virtio'/></disk>`+
		`<interface type='bridge'><mac address='00:50:56:3f:a8:21'/><model type='virtio'/></interface></devices></domain>`), 0o644)
	out.Disks, out.DomainXML = []string{disk}, dom
	return out
}

// After the download: the conversion's outcome, the guest OS virt-v2v found and the firmware decide the steps.
func TestConvertStep(t *testing.T) {
	ctx := context.Background()
	const linux, win = "http://debian.org/debian/12", "http://microsoft.com/win/2k22"
	for _, tc := range []struct {
		name       string
		conv       fakeConverter
		guestID    string
		srcFW      string
		conversion string
		firmware   string
		boots      bool
		want       string
	}{
		{"linux bios", fakeConverter{"converted", linux, "bios", false}, "debian12_64Guest", "bios", verify.OK, verify.OK, true, "BIOS on the source and in the sandbox"},
		{"linux uefi", fakeConverter{"converted", linux, "efi", false}, "debian12_64Guest", "efi", verify.OK, verify.OK, true, "UEFI on the source and in the sandbox"},
		{"firmware changed", fakeConverter{"converted", linux, "bios", false}, "debian12_64Guest", "efi", verify.OK, verify.Critical, true, "the source boots with efi, the converted guest with bios"},
		{"windows with drivers", fakeConverter{"converted", win, "bios", true}, "windows2019srvNext_64Guest", "bios", verify.OK, verify.OK, true, "virtio-win drivers"},
		{"windows the source did not name", fakeConverter{"converted", win, "bios", false}, "otherGuest64", "bios", verify.Unknown, verify.Unknown, false, "no virtio-win drivers were given"},
		{"refused", fakeConverter{state: "failed"}, "debian12_64Guest", "bios", verify.Critical, verify.Unknown, false, "fake failed"},
		{"not run", fakeConverter{state: "not_run"}, "debian12_64Guest", "bios", verify.Unknown, verify.Unknown, false, "fake not_run"},
	} {
		dir := t.TempDir()
		x := &vmRun{o: Options{Converter: tc.conv}, name: "vm1", vm: vsphereread.VM{GuestID: tc.guestID, Firmware: tc.srcFW},
			work: dir, images: dir, extra: map[string]string{}, t: map[string]float64{}}
		disks, cfg, skip := x.convertStep(ctx, filepath.Join(dir, "vm1.vmx"))
		got := map[string]verify.CheckResult{}
		for _, c := range x.pre {
			got[c.Name] = c
		}
		all := ""
		for _, c := range x.pre {
			all += c.Name + " " + c.Verdict + ": " + c.Detail + "\n"
		}
		if got["conversion"].Verdict != tc.conversion || got["firmware"].Verdict != tc.firmware || (len(disks) > 0) != tc.boots ||
			(skip == "") != tc.boots || !strings.Contains(all, tc.want) {
			t.Errorf("%s: disks %v skip %q\n%s", tc.name, disks, skip, all)
		}
		if tc.boots && (cfg == nil || cfg.Machine.Firmware != tc.conv.firmware) {
			t.Errorf("%s: config %+v", tc.name, cfg)
		}
	}
}
