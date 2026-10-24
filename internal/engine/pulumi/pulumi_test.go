package pulumi

import (
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
)

// mocks records the inputs of each resource the program registers.
type mocks struct {
	mu     sync.Mutex
	inputs map[string]resource.PropertyMap
}

func (m *mocks) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputs[args.TypeToken+"::"+args.Name] = args.Inputs
	out := args.Inputs.Copy()
	if v, ok := args.Inputs["vmId"]; ok {
		out["vmId"] = v
	}
	return args.Name + "-id", out, nil
}

func (m *mocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return args.Args, nil
}

func TestProgram(t *testing.T) {
	st := Stack{
		Name:    "poc-web-lab",
		Proxmox: proxmox.Credentials{Endpoint: "https://192.168.1.99:8006", TokenID: "gaia@pve!poc", TokenSecret: "s3cret", Insecure: true},
		VMs: []VM{{
			UID: "abcdefgh23456789", Hostname: "labtpoc01", VMID: 9400, Node: "proxmox", Pool: "gaia", Storage: "local-lvm",
			Template: 9300, CPU: 2, MemoryMiB: 2048, DiskGiB: 20, ExtraDisks: []int{50}, Bridge: "gaiapoc",
			Address: "10.50.0.100/24", Gateway: "10.50.0.1", DNS: []string{"1.1.1.1"}, User: "gaia", SSHKeys: []string{"ssh-ed25519 AAAA gaia"},
			Tags: []string{"gaia", "env_test"}, Description: "gaia.uid=abcdefgh23456789", SMBIOSUUID: "0f1e2d3c-4b5a-8978-8695-a4b3c2d1e0f0",
		}},
	}
	m := &mocks{inputs: map[string]resource.PropertyMap{}}
	if err := pulumi.RunErr(Program(st), pulumi.WithMocks(Project, st.Name, m)); err != nil {
		t.Fatal(err)
	}

	prov := m.inputs["pulumi:providers:proxmoxve::proxmox"]
	if prov == nil || prov["endpoint"].StringValue() != "https://192.168.1.99:8006" || !prov["insecure"].BoolValue() {
		t.Fatalf("provider inputs = %v", prov)
	}
	tok := prov["apiToken"]
	if tok.IsSecret() {
		tok = tok.SecretValue().Element
	}
	if !tok.IsString() || tok.StringValue() != "gaia@pve!poc=s3cret" {
		t.Errorf("apiToken = %v", prov["apiToken"])
	}

	vm := m.inputs["proxmoxve:index/vmLegacy:VmLegacy::vm-abcdefgh23456789"]
	if vm == nil {
		t.Fatalf("no VM registered; got %v", keys(m.inputs))
	}
	check := func(key resource.PropertyKey, want any) {
		t.Helper()
		got := vm[key]
		switch w := want.(type) {
		case string:
			if !got.IsString() || got.StringValue() != w {
				t.Errorf("%s = %v, want %q", key, got, w)
			}
		case float64:
			if !got.IsNumber() || got.NumberValue() != w {
				t.Errorf("%s = %v, want %v", key, got, w)
			}
		}
	}
	check("name", "labtpoc01")
	check("vmId", 9400.0)
	check("nodeName", "proxmox")
	check("poolId", "gaia")
	check("description", "gaia.uid=abcdefgh23456789")
	if clone := vm["clone"].ObjectValue(); clone["vmId"].NumberValue() != 9300 || !clone["full"].BoolValue() {
		t.Errorf("clone = %v", clone)
	}
	disks := vm["disks"].ArrayValue()
	if len(disks) != 2 || disks[0].ObjectValue()["size"].NumberValue() != 20 || disks[1].ObjectValue()["interface"].StringValue() != "scsi1" {
		t.Errorf("disks = %v", disks)
	}
	init := vm["initialization"].ObjectValue()
	ipv4 := init["ipConfigs"].ArrayValue()[0].ObjectValue()["ipv4"].ObjectValue()
	if ipv4["address"].StringValue() != "10.50.0.100/24" || ipv4["gateway"].StringValue() != "10.50.0.1" {
		t.Errorf("ipv4 = %v", ipv4)
	}
	if u := init["userAccount"].ObjectValue(); u["username"].StringValue() != "gaia" || u["keys"].ArrayValue()[0].StringValue() != "ssh-ed25519 AAAA gaia" {
		t.Errorf("userAccount = %v", u)
	}
	if nic := vm["networkDevices"].ArrayValue()[0].ObjectValue(); nic["bridge"].StringValue() != "gaiapoc" {
		t.Errorf("nic = %v", nic)
	}
	if s := vm["smbios"].ObjectValue(); s["uuid"].StringValue() != "0f1e2d3c-4b5a-8978-8695-a4b3c2d1e0f0" {
		t.Errorf("smbios = %v", s)
	}
}

func TestChanges(t *testing.T) {
	if (Changes{"same": 3}).Changed() {
		t.Error("only same is a change")
	}
	c := Changes{"same": 1, "create": 2}
	if !c.Changed() || c.String() != "create 2, same 1" {
		t.Errorf("%v %q", c.Changed(), c.String())
	}
}

func keys(m map[string]resource.PropertyMap) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
