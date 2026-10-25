package pulumi

import (
	"encoding/base64"
	"encoding/json"
	"strings"
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

// vcMocks answer vSphere's lookups as a vCenter would.
type vcMocks struct{ mocks }

func (m *vcMocks) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	out := args.Args.Copy()
	switch args.Token {
	case "vsphere:index/getDatacenter:getDatacenter":
		out["id"] = resource.NewStringProperty("datacenter-1")
	case "vsphere:index/getDatastore:getDatastore":
		out["id"] = resource.NewStringProperty("datastore-12")
	case "vsphere:index/getComputeCluster:getComputeCluster":
		out["id"] = resource.NewStringProperty("domain-c7")
		out["resourcePoolId"] = resource.NewStringProperty("resgroup-8")
	case "vsphere:index/getNetwork:getNetwork":
		out["id"] = resource.NewStringProperty("dvportgroup-20")
	case "vsphere:index/getVirtualMachine:getVirtualMachine":
		out["id"] = resource.NewStringProperty("tpl-uuid")
		out["guestId"] = resource.NewStringProperty("rhel9_64Guest")
		out["networkInterfaceTypes"] = resource.NewArrayProperty([]resource.PropertyValue{resource.NewStringProperty("vmxnet3")})
	}
	return out, nil
}

func TestVSphereProgram(t *testing.T) {
	st := Stack{
		Name:    "portal-web-ank",
		VSphere: &VSphereCredentials{Server: "vcenter.corp.local", Username: "gaia@vsphere.local", Password: "s3cret"},
		VMs: []VM{{
			UID: "abcdefgh23456789", Hostname: "ankpweb01", CPU: 4, MemoryMiB: 8192, DiskGiB: 80, ExtraDisks: []int{100},
			Address: "10.20.1.15/24", Gateway: "10.20.1.1", DNS: []string{"10.0.0.53"}, Description: "Managed by Gaia.",
			Datacenter: "DC-ANK", Cluster: "CL-PROD", Datastore: "DS-PROD-01", Folder: "Gaia/portal", Portgroup: "dvPG-VLAN120",
			TemplateName: "tpl-rhel9-latest", Domain: "corp.local",
		}},
	}
	m := &vcMocks{mocks{inputs: map[string]resource.PropertyMap{}}}
	if err := pulumi.RunErr(Program(st), pulumi.WithMocks(Project, st.Name, m)); err != nil {
		t.Fatal(err)
	}
	vm := m.inputs["vsphere:index/virtualMachine:VirtualMachine::vm-abcdefgh23456789"]
	if vm == nil {
		t.Fatalf("no VM: %v", m.inputs)
	}
	if vm["name"].StringValue() != "ankpweb01" || vm["resourcePoolId"].StringValue() != "resgroup-8" || vm["datastoreId"].StringValue() != "datastore-12" ||
		vm["folder"].StringValue() != "Gaia/portal" || vm["guestId"].StringValue() != "rhel9_64Guest" || vm["numCpus"].NumberValue() != 4 {
		t.Errorf("vm = %v", vm)
	}
	clone := vm["clone"].ObjectValue()
	cust := clone["customize"].ObjectValue()
	nic := cust["networkInterfaces"].ArrayValue()[0].ObjectValue()
	if clone["templateUuid"].StringValue() != "tpl-uuid" || nic["ipv4Address"].StringValue() != "10.20.1.15" || nic["ipv4Netmask"].NumberValue() != 24 ||
		cust["ipv4Gateway"].StringValue() != "10.20.1.1" || cust["linuxOptions"].ObjectValue()["hostName"].StringValue() != "ankpweb01" {
		t.Errorf("clone = %v", clone)
	}
	if disks := vm["disks"].ArrayValue(); len(disks) != 2 || disks[1].ObjectValue()["size"].NumberValue() != 100 {
		t.Errorf("disks = %v", vm["disks"])
	}
	if n := vm["networkInterfaces"].ArrayValue()[0].ObjectValue(); n["networkId"].StringValue() != "dvportgroup-20" || n["adapterType"].StringValue() != "vmxnet3" {
		t.Errorf("nic = %v", n)
	}
}

func TestFlatten(t *testing.T) {
	if got := flatten([]any{[]any{"10.0.0.1", "fe80::1"}, []any{}}); len(got) != 2 {
		t.Errorf("proxmox shape = %v", got)
	}
	if got := flatten([]any{"10.0.0.2"}); len(got) != 1 {
		t.Errorf("vsphere shape = %v", got)
	}
}

func TestGuestinfo(t *testing.T) {
	v := VM{UID: "u1", Hostname: "h1", Gateway: "10.0.0.1", DNS: []string{"10.0.0.2"}, Domain: "lab.test", User: "gaia", SSHKeys: []string{"ssh-ed25519 K"}}
	meta, user := guestinfo(v, "10.0.0.5", 24)
	m, _ := base64.StdEncoding.DecodeString(meta)
	u, _ := base64.StdEncoding.DecodeString(user)
	for _, want := range []string{`"local-hostname":"h1"`, `"addresses":["10.0.0.5/24"]`, `"gateway4":"10.0.0.1"`, `"search":["lab.test"]`} {
		if !strings.Contains(string(m), want) {
			t.Errorf("metadata lacks %s: %s", want, m)
		}
	}
	if !strings.HasPrefix(string(u), "#cloud-config\n") || !strings.Contains(string(u), `"ssh_authorized_keys":["ssh-ed25519 K"]`) {
		t.Errorf("userdata = %s", u)
	}
}

func TestForget(t *testing.T) {
	dep := json.RawMessage(`{"manifest":{"time":"x"},"resources":[
		{"urn":"urn:pulumi:poc-web-lab::gaia::pulumi:pulumi:Stack::gaia-poc-web-lab","type":"pulumi:pulumi:Stack"},
		{"urn":"urn:pulumi:poc-web-lab::gaia::proxmoxve:index/vmLegacy:VmLegacy::vm-u1","type":"proxmoxve:index/vmLegacy:VmLegacy","id":"9401"},
		{"urn":"urn:pulumi:poc-web-lab::gaia::proxmoxve:index/vmLegacy:VmLegacy::vm-u2","type":"proxmoxve:index/vmLegacy:VmLegacy","id":"9402"}]}`)
	out, found, err := forget(dep, []string{"u1", "u9"})
	if err != nil || len(found) != 1 || found[0] != "u1" {
		t.Fatalf("found = %v %v", found, err)
	}
	if s := string(out); strings.Contains(s, "vm-u1") || !strings.Contains(s, "vm-u2") || !strings.Contains(s, `"manifest"`) {
		t.Errorf("deployment = %s", s)
	}
	if same, found, _ := forget(dep, []string{"u9"}); len(found) != 0 || string(same) != string(dep) {
		t.Error("nothing to forget changed the deployment")
	}
}

func TestProviderNoise(t *testing.T) {
	for k, noise := range map[string]bool{"disks[0].speed": true, "disks[1].speed.read": true, "disks[0].size": false, "memory.dedicated": false} {
		if providerNoise.MatchString(k) != noise {
			t.Errorf("%s: noise %v", k, !noise)
		}
	}
}
