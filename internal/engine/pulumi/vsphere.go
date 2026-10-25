package pulumi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/pulumi/pulumi-vsphere/sdk/v4/go/vsphere"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// The vSphere provider plugin; the version matches the SDK in go.mod.
const (
	vspherePlugin        = "vsphere"
	vspherePluginVersion = "v4.18.1"
)

// VSphereCredentials reach vCenter.
type VSphereCredentials struct {
	Server   string // host name of vCenter
	Username string
	Password string
	Insecure bool
}

// vsphereProgram clones each VM from its template, customized with its
// name, address, gateway and DNS (design 4.2).
func vsphereProgram(st Stack) pulumi.RunFunc {
	return func(ctx *pulumi.Context) error {
		vc := st.VSphere
		prov, err := vsphere.NewProvider(ctx, "vsphere", &vsphere.ProviderArgs{
			User: pulumi.String(vc.Username), Password: pulumi.StringPtr(vc.Password),
			VsphereServer: pulumi.String(vc.Server), AllowUnverifiedSsl: pulumi.Bool(vc.Insecure),
		})
		if err != nil {
			return err
		}
		inv := pulumi.Provider(prov)
		uuids := pulumi.StringMap{}
		addrs := pulumi.StringArrayMap{}
		for _, v := range st.VMs {
			dc, err := vsphere.LookupDatacenter(ctx, &vsphere.LookupDatacenterArgs{Name: &v.Datacenter}, inv)
			if err != nil {
				return fmt.Errorf("%s: datacenter %s: %w", v.Hostname, v.Datacenter, err)
			}
			ds, err := vsphere.GetDatastore(ctx, &vsphere.GetDatastoreArgs{Name: v.Datastore, DatacenterId: &dc.Id}, inv)
			if err != nil {
				return fmt.Errorf("%s: datastore %s: %w", v.Hostname, v.Datastore, err)
			}
			cl, err := vsphere.LookupComputeCluster(ctx, &vsphere.LookupComputeClusterArgs{Name: v.Cluster, DatacenterId: &dc.Id}, inv)
			if err != nil {
				return fmt.Errorf("%s: cluster %s: %w", v.Hostname, v.Cluster, err)
			}
			net, err := vsphere.GetNetwork(ctx, &vsphere.GetNetworkArgs{Name: v.Portgroup, DatacenterId: &dc.Id}, inv)
			if err != nil {
				return fmt.Errorf("%s: portgroup %s: %w", v.Hostname, v.Portgroup, err)
			}
			tpl, err := vsphere.LookupVirtualMachine(ctx, &vsphere.LookupVirtualMachineArgs{Name: &v.TemplateName, DatacenterId: &dc.Id}, inv)
			if err != nil {
				return fmt.Errorf("%s: template %s: %w", v.Hostname, v.TemplateName, err)
			}
			vm, err := vsphere.NewVirtualMachine(ctx, "vm-"+v.UID, vsphereArgs(v, cl.ResourcePoolId, ds.Id, net.Id, tpl), pulumi.Provider(prov))
			if err != nil {
				return err
			}
			uuids[v.UID] = vm.Uuid
			addrs[v.UID] = vm.GuestIpAddresses
		}
		ctx.Export("uuids", uuids)
		ctx.Export("ipv4", addrs)
		return nil
	}
}

func vsphereArgs(v VM, pool, datastore, network string, tpl *vsphere.LookupVirtualMachineResult) *vsphere.VirtualMachineArgs {
	adapter := "vmxnet3"
	if len(tpl.NetworkInterfaceTypes) > 0 {
		adapter = tpl.NetworkInterfaceTypes[0]
	}
	prefix := 24
	ip := v.Address
	if p, err := netip.ParsePrefix(v.Address); err == nil {
		prefix, ip = p.Bits(), p.Addr().String()
	}
	disks := vsphere.VirtualMachineDiskArray{&vsphere.VirtualMachineDiskArgs{Label: pulumi.String("disk0"), Size: pulumi.Int(v.DiskGiB), ThinProvisioned: pulumi.Bool(true)}}
	for i, size := range v.ExtraDisks {
		disks = append(disks, &vsphere.VirtualMachineDiskArgs{Label: pulumi.Sprintf("disk%d", i+1), Size: pulumi.Int(size), UnitNumber: pulumi.Int(i + 1), ThinProvisioned: pulumi.Bool(true)})
	}
	args := &vsphere.VirtualMachineArgs{
		Name: pulumi.String(v.Hostname), ResourcePoolId: pulumi.String(pool), DatastoreId: pulumi.String(datastore),
		NumCpus: pulumi.Int(v.CPU), Memory: pulumi.Int(v.MemoryMiB), GuestId: pulumi.String(tpl.GuestId),
		NetworkInterfaces: vsphere.VirtualMachineNetworkInterfaceArray{&vsphere.VirtualMachineNetworkInterfaceArgs{
			NetworkId: pulumi.String(network), AdapterType: pulumi.String(adapter)}},
		Disks:      disks,
		Annotation: pulumi.String(v.Description),
		Clone: &vsphere.VirtualMachineCloneArgs{
			TemplateUuid: pulumi.String(tpl.Id),
			Customize: &vsphere.VirtualMachineCloneCustomizeArgs{
				LinuxOptions: &vsphere.VirtualMachineCloneCustomizeLinuxOptionsArgs{HostName: pulumi.String(v.Hostname), Domain: pulumi.String(v.Domain)},
				NetworkInterfaces: vsphere.VirtualMachineCloneCustomizeNetworkInterfaceArray{&vsphere.VirtualMachineCloneCustomizeNetworkInterfaceArgs{
					Ipv4Address: pulumi.String(ip), Ipv4Netmask: pulumi.Int(prefix)}},
				Ipv4Gateway:    pulumi.String(v.Gateway),
				DnsServerLists: pulumi.ToStringArray(v.DNS),
			},
		},
		WaitForGuestNetTimeout: pulumi.Int(10),
	}
	if v.Folder != "" {
		args.Folder = pulumi.String(v.Folder)
	}
	if v.CloudInit {
		args.Clone.(*vsphere.VirtualMachineCloneArgs).Customize = nil
		meta, user := guestinfo(v, ip, prefix)
		args.ExtraConfig = pulumi.StringMap{
			"guestinfo.metadata": pulumi.String(meta), "guestinfo.metadata.encoding": pulumi.String("base64"),
			"guestinfo.userdata": pulumi.String(user), "guestinfo.userdata.encoding": pulumi.String("base64"),
		}
	}
	return args
}

// guestinfo is cloud-init's metadata (name and network) and user data
// (the login user and its keys), base64 encoded. JSON is YAML, so both
// are written as JSON.
func guestinfo(v VM, ip string, prefix int) (meta, user string) {
	nameservers := map[string]any{"addresses": v.DNS}
	if v.Domain != "" {
		nameservers["search"] = []string{v.Domain}
	}
	m, _ := json.Marshal(map[string]any{
		"instance-id": v.UID, "local-hostname": v.Hostname,
		"network": map[string]any{"version": 2, "ethernets": map[string]any{"nic0": map[string]any{
			"match": map[string]string{"name": "e*"}, "addresses": []string{fmt.Sprintf("%s/%d", ip, prefix)},
			"gateway4": v.Gateway, "nameservers": nameservers,
		}}},
	})
	u, _ := json.Marshal(map[string]any{
		"users": []map[string]any{{"name": v.User, "sudo": "ALL=(ALL) NOPASSWD:ALL", "shell": "/bin/bash", "ssh_authorized_keys": v.SSHKeys}},
	})
	return base64.StdEncoding.EncodeToString(m), base64.StdEncoding.EncodeToString(append([]byte("#cloud-config\n"), u...))
}
