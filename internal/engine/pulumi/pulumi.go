package pulumi

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/muhlba91/pulumi-proxmoxve/sdk/v8/go/proxmoxve"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
)

// Project is the Pulumi project of every Gaia stack.
const Project = "gaia"

// The Proxmox provider plugin; the version matches the SDK in go.mod.
const (
	proxmoxPlugin        = "proxmoxve"
	proxmoxPluginVersion = "v8.6.0"
	proxmoxPluginServer  = "github://api.github.com/muhlba91"
)

// Backend is where stacks keep their state and how secrets in it are
// encrypted.
type Backend struct {
	URL             string            // s3://gaia-state?... (SeaweedFS)
	SecretsProvider string            // hashivault://pulumi (OpenBao transit)
	Env             map[string]string // credentials for both
}

// VM is one Proxmox VM of a stack.
type VM struct {
	UID         string
	Hostname    string
	VMID        int
	Node        string
	Pool        string
	Storage     string
	Template    int // VM ID of the template
	CPU         int
	MemoryMiB   int
	DiskGiB     int
	ExtraDisks  []int // GiB, as scsi1, scsi2...
	Bridge      string
	VLAN        int
	Address     string // CIDR form: 10.50.0.100/24
	Gateway     string
	DNS         []string
	User        string
	SSHKeys     []string
	Tags        []string
	Description string
	SMBIOSUUID  string
}

// Stack is the VMs of one contract on one site.
type Stack struct {
	Name    string
	Proxmox proxmox.Credentials
	VMs     []VM
}

// Changes counts resource operations: create, update, delete, same...
type Changes map[string]int

// Program returns the stack's Pulumi program.
func Program(st Stack) pulumi.RunFunc {
	return func(ctx *pulumi.Context) error {
		prov, err := proxmoxve.NewProvider(ctx, "proxmox", &proxmoxve.ProviderArgs{
			Endpoint: pulumi.String(st.Proxmox.Endpoint),
			ApiToken: pulumi.String(st.Proxmox.APIToken()),
			Insecure: pulumi.Bool(st.Proxmox.Insecure),
		})
		if err != nil {
			return err
		}
		vmids := pulumi.IntMap{}
		for _, v := range st.VMs {
			vm, err := proxmoxve.NewVmLegacy(ctx, "vm-"+v.UID, vmArgs(v), pulumi.Provider(prov))
			if err != nil {
				return err
			}
			vmids[v.UID] = vm.VmId
		}
		ctx.Export("vmids", vmids)
		return nil
	}
}

func vmArgs(v VM) *proxmoxve.VmLegacyArgs {
	disks := proxmoxve.VmLegacyDiskArray{&proxmoxve.VmLegacyDiskArgs{
		Interface: pulumi.String("scsi0"), Size: pulumi.Int(v.DiskGiB), DatastoreId: pulumi.String(v.Storage),
	}}
	for i, size := range v.ExtraDisks {
		disks = append(disks, &proxmoxve.VmLegacyDiskArgs{
			Interface: pulumi.String(fmt.Sprintf("scsi%d", i+1)), Size: pulumi.Int(size), DatastoreId: pulumi.String(v.Storage),
		})
	}
	nic := &proxmoxve.VmLegacyNetworkDeviceArgs{Bridge: pulumi.String(v.Bridge), Model: pulumi.String("virtio")}
	if v.VLAN != 0 {
		nic.VlanId = pulumi.Int(v.VLAN)
	}
	ipv4 := &proxmoxve.VmLegacyInitializationIpConfigIpv4Args{Address: pulumi.String(v.Address)}
	if v.Gateway != "" {
		ipv4.Gateway = pulumi.String(v.Gateway)
	}
	init := &proxmoxve.VmLegacyInitializationArgs{
		DatastoreId: pulumi.String(v.Storage),
		IpConfigs:   proxmoxve.VmLegacyInitializationIpConfigArray{&proxmoxve.VmLegacyInitializationIpConfigArgs{Ipv4: ipv4}},
		UserAccount: &proxmoxve.VmLegacyInitializationUserAccountArgs{
			Username: pulumi.String(v.User), Keys: pulumi.ToStringArray(v.SSHKeys),
		},
	}
	if len(v.DNS) > 0 {
		init.Dns = &proxmoxve.VmLegacyInitializationDnsArgs{Servers: pulumi.ToStringArray(v.DNS)}
	}
	return &proxmoxve.VmLegacyArgs{
		Name:            pulumi.String(v.Hostname),
		NodeName:        pulumi.String(v.Node),
		VmId:            pulumi.Int(v.VMID),
		PoolId:          pulumi.String(v.Pool),
		Description:     pulumi.String(v.Description),
		Tags:            pulumi.ToStringArray(v.Tags),
		Clone:           &proxmoxve.VmLegacyCloneArgs{VmId: pulumi.Int(v.Template), Full: pulumi.Bool(true), DatastoreId: pulumi.String(v.Storage)},
		Cpu:             &proxmoxve.VmLegacyCpuArgs{Cores: pulumi.Int(v.CPU), Type: pulumi.String("host")},
		Memory:          &proxmoxve.VmLegacyMemoryArgs{Dedicated: pulumi.Int(v.MemoryMiB)},
		Disks:           disks,
		ScsiHardware:    pulumi.String("virtio-scsi-single"),
		NetworkDevices:  proxmoxve.VmLegacyNetworkDeviceArray{nic},
		Initialization:  init,
		Agent:           &proxmoxve.VmLegacyAgentArgs{Enabled: pulumi.Bool(true)},
		Smbios:          &proxmoxve.VmLegacySmbiosArgs{Uuid: pulumi.String(v.SMBIOSUUID)},
		OperatingSystem: &proxmoxve.VmLegacyOperatingSystemArgs{Type: pulumi.String("l26")},
		OnBoot:          pulumi.Bool(true),
		Started:         pulumi.Bool(true),
	}
}

func stack(ctx context.Context, b Backend, st Stack) (auto.Stack, error) {
	proj := auto.Project(workspace.Project{
		Name:    tokens.PackageName(Project),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{URL: b.URL},
	})
	s, err := auto.UpsertStackInlineSource(ctx, st.Name, Project, Program(st),
		proj, auto.SecretsProvider(b.SecretsProvider), auto.EnvVars(b.Env))
	if err != nil {
		return auto.Stack{}, fmt.Errorf("stack %s: %w", st.Name, err)
	}
	if err := s.Workspace().InstallPluginFromServer(ctx, proxmoxPlugin, proxmoxPluginVersion, proxmoxPluginServer); err != nil {
		return auto.Stack{}, fmt.Errorf("install %s plugin: %w", proxmoxPlugin, err)
	}
	return s, nil
}

// Up brings the stack to the program and returns each VM's ID by UID and
// what changed. Progress goes to w.
func Up(ctx context.Context, b Backend, st Stack, w io.Writer) (map[string]int, Changes, error) {
	s, err := stack(ctx, b, st)
	if err != nil {
		return nil, nil, err
	}
	res, err := s.Up(ctx, optup.ProgressStreams(w), optup.SuppressProgress())
	if err != nil {
		return nil, nil, fmt.Errorf("pulumi up %s: %w", st.Name, err)
	}
	vmids := map[string]int{}
	if out, ok := res.Outputs["vmids"].Value.(map[string]any); ok {
		for uid, v := range out {
			if f, ok := v.(float64); ok {
				vmids[uid] = int(f)
			}
		}
	}
	changes := Changes{}
	if res.Summary.ResourceChanges != nil {
		for k, n := range *res.Summary.ResourceChanges {
			changes[k] = n
		}
	}
	return vmids, changes, nil
}

// Preview returns what Up would change, without changing anything.
func Preview(ctx context.Context, b Backend, st Stack, w io.Writer) (Changes, error) {
	s, err := stack(ctx, b, st)
	if err != nil {
		return nil, err
	}
	res, err := s.Preview(ctx, optpreview.ProgressStreams(w), optpreview.SuppressProgress())
	if err != nil {
		return nil, fmt.Errorf("pulumi preview %s: %w", st.Name, err)
	}
	changes := Changes{}
	for op, n := range res.ChangeSummary {
		changes[string(op)] = n
	}
	return changes, nil
}

// String prints changes as "create 1, same 2".
func (c Changes) String() string {
	var keys []string
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s %d", k, c[k])
	}
	return out
}

// Changed reports whether anything but "same" happened.
func (c Changes) Changed() bool {
	for k, n := range c {
		if k != "same" && n > 0 {
			return true
		}
	}
	return false
}
