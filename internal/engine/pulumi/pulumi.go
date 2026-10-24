package pulumi

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/blang/semver"

	"github.com/muhlba91/pulumi-proxmoxve/sdk/v8/go/proxmoxve"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/debug"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
)

// Project is the Pulumi project of every Gaia stack.
const Project = "gaia"

// cliVersion is the Pulumi CLI Gaia runs, the version of the SDK in
// go.mod. Gaia installs it rather than using whatever pulumi is on PATH:
// the CLI's state backend reads its URL parameters through its own
// gocloud version, so a different CLI understands different parameters.
var cliVersion = semver.MustParse("3.272.0")

// The Proxmox provider plugin; the version matches the SDK in go.mod.
const (
	proxmoxPlugin        = "proxmoxve"
	proxmoxPluginVersion = "v8.6.0"
	proxmoxPluginServer  = "github://api.github.com/muhlba91"
)

// Backend is where stacks keep their state and how secrets in it are
// encrypted.
type Backend struct {
	URL             string            // postgres://... or s3://...
	SecretsProvider string            // hashivault://pulumi (OpenBao transit)
	Env             map[string]string // credentials for both
	Settings        SettingsStore     // where stack secrets settings are kept
}

// Settings are a stack's secrets settings: the provider and the data key,
// encrypted by it.
type Settings struct {
	SecretsProvider string
	EncryptedKey    string
}

// SettingsStore keeps stack settings between runs: an inline program's
// workspace is temporary, and Pulumi would otherwise fall back to a
// passphrase it does not have.
type SettingsStore interface {
	LoadStack(ctx context.Context, name string) (Settings, bool, error)
	SaveStack(ctx context.Context, name string, s Settings) error
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
		addrs := pulumi.StringArrayArrayMap{}
		for _, v := range st.VMs {
			vm, err := proxmoxve.NewVmLegacy(ctx, "vm-"+v.UID, vmArgs(v), pulumi.Provider(prov))
			if err != nil {
				return err
			}
			vmids[v.UID] = vm.VmId
			addrs[v.UID] = vm.Ipv4Addresses
		}
		ctx.Export("vmids", vmids)
		// The addresses the guest agent reports, per interface.
		ctx.Export("ipv4", addrs)
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

// command returns the pinned Pulumi CLI, installing it under the user's
// cache directory the first time.
func command(ctx context.Context) (auto.PulumiCommand, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	opts := &auto.PulumiCommandOptions{Version: cliVersion, Root: filepath.Join(cache, "gaia", "pulumi-"+cliVersion.String())}
	if cmd, err := auto.NewPulumiCommand(opts); err == nil {
		return cmd, nil
	}
	cmd, err := auto.InstallPulumiCommand(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("install pulumi %s: %w", cliVersion, err)
	}
	return cmd, nil
}

func stack(ctx context.Context, b Backend, st Stack) (auto.Stack, error) {
	cmd, err := command(ctx)
	if err != nil {
		return auto.Stack{}, err
	}
	proj := auto.Project(workspace.Project{
		Name:    tokens.PackageName(Project),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{URL: b.URL},
	})
	ws, err := auto.NewLocalWorkspace(ctx, auto.Program(Program(st)), auto.Pulumi(cmd), proj,
		auto.SecretsProvider(b.SecretsProvider), auto.EnvVars(b.Env))
	if err != nil {
		return auto.Stack{}, err
	}
	saved, ok, err := b.Settings.LoadStack(ctx, st.Name)
	if err != nil {
		return auto.Stack{}, err
	}
	var s auto.Stack
	if ok {
		if err := ws.SaveStackSettings(ctx, st.Name, &workspace.ProjectStack{SecretsProvider: saved.SecretsProvider, EncryptedKey: saved.EncryptedKey}); err != nil {
			return auto.Stack{}, err
		}
		// A stack missing from the backend (a new backend, a removed
		// stack) is created again with the saved settings.
		if s, err = auto.UpsertStack(ctx, st.Name, ws); err != nil {
			return auto.Stack{}, fmt.Errorf("stack %s: %w", st.Name, err)
		}
	} else {
		if s, err = auto.UpsertStack(ctx, st.Name, ws); err != nil {
			return auto.Stack{}, fmt.Errorf("stack %s: %w", st.Name, err)
		}
		ps, err := ws.StackSettings(ctx, st.Name)
		if err != nil {
			return auto.Stack{}, err
		}
		if ps.SecretsProvider == "" || ps.EncryptedKey == "" {
			return auto.Stack{}, fmt.Errorf("stack %s: created without %s secrets", st.Name, b.SecretsProvider)
		}
		if err := b.Settings.SaveStack(ctx, st.Name, Settings{SecretsProvider: ps.SecretsProvider, EncryptedKey: ps.EncryptedKey}); err != nil {
			return auto.Stack{}, err
		}
	}
	if err := s.Workspace().InstallPluginFromServer(ctx, proxmoxPlugin, proxmoxPluginVersion, proxmoxPluginServer); err != nil {
		return auto.Stack{}, fmt.Errorf("install %s plugin: %w", proxmoxPlugin, err)
	}
	return s, nil
}

// Result is what Up reports per VM, by UID.
type Result struct {
	VMID int
	IPv4 []string // the addresses the guest agent reports
}

// Up brings the stack to the program and returns each VM's ID and
// addresses by UID, and what changed. It refreshes first: what is compared
// with the program is the VMs as they are, not as the last run left them.
// Progress goes to w.
func Up(ctx context.Context, b Backend, st Stack, w io.Writer) (map[string]Result, Changes, error) {
	s, err := stack(ctx, b, st)
	if err != nil {
		return nil, nil, err
	}
	opts := []optup.Option{optup.ProgressStreams(w), optup.SuppressProgress(), optup.Refresh()}
	if lvl, ok := debugLevel(); ok {
		opts = append(opts, optup.DebugLogging(debug.LoggingOptions{LogLevel: &lvl, FlowToPlugins: true, Debug: true}), optup.ErrorProgressStreams(w))
	}
	res, err := s.Up(ctx, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("pulumi up %s: %w", st.Name, err)
	}
	vms := map[string]Result{}
	if out, ok := res.Outputs["vmids"].Value.(map[string]any); ok {
		for uid, v := range out {
			if f, ok := v.(float64); ok {
				vms[uid] = Result{VMID: int(f)}
			}
		}
	}
	if out, ok := res.Outputs["ipv4"].Value.(map[string]any); ok {
		for uid, ifaces := range out {
			r := vms[uid]
			list, _ := ifaces.([]any)
			for _, iface := range list {
				addrs, _ := iface.([]any)
				for _, a := range addrs {
					if s, ok := a.(string); ok {
						r.IPv4 = append(r.IPv4, s)
					}
				}
			}
			vms[uid] = r
		}
	}
	changes := Changes{}
	if res.Summary.ResourceChanges != nil {
		for k, n := range *res.Summary.ResourceChanges {
			changes[k] = n
		}
	}
	return vms, changes, nil
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

// debugLevel is $GAIA_PULUMI_DEBUG: a Pulumi log level for both the CLI
// and the providers. The logs may hold secrets; use it on a lab only.
func debugLevel() (uint, bool) {
	v := os.Getenv("GAIA_PULUMI_DEBUG")
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 8)
	if err != nil || n == 0 {
		return 9, true
	}
	return uint(n), true
}
