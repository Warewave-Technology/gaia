package vsphereread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// File is one file of a VM, from its layoutEx.
type File struct {
	Name string `json:"name"` // datastore path: "[datastore1] vm/vm.vmx"
	Type string `json:"type"` // config, diskDescriptor, diskExtent, nvram, log, ...
	Size int64  `json:"size"`
}

// Disk is one virtual disk.
type Disk struct {
	Label    string `json:"label"`
	File     string `json:"file"` // datastore path of the descriptor
	Capacity int64  `json:"capacity_bytes"`
	Thin     bool   `json:"thin"`
}

// NIC is one network adapter.
type NIC struct {
	Label string `json:"label"`
	Type  string `json:"type"` // e.g. VirtualVmxnet3
	MAC   string `json:"mac"`
}

// VM is what the inventory knows about a virtual machine.
type VM struct {
	Name       string `json:"name"`
	UUID       string `json:"uuid"`
	PowerState string `json:"power_state"`
	GuestID    string `json:"guest_id"`
	GuestName  string `json:"guest_full_name"`
	Firmware   string `json:"firmware"`
	SecureBoot bool   `json:"secure_boot"` // UEFI Secure Boot is on (bootOptions.efiSecureBootEnabled)
	CPUs       int32  `json:"cpus"`
	MemoryMiB  int32  `json:"memory_mib"`
	VMX        string `json:"vmx"` // datastore path
	Datacenter string `json:"datacenter"`
	Disks      []Disk `json:"disks"`
	NICs       []NIC  `json:"nics"`
	Files      []File `json:"files"`
	Snapshots  bool   `json:"has_snapshots"`
	Template   bool   `json:"template"`
	Unreadable string `json:"unreadable,omitempty"` // why the config could not be read
	ObjectRef  string `json:"moref"`
}

// Inventory lists every VM under the root folder with one property-collector read.
// Stage 1 supports a single datacenter (a standalone ESXi host has "ha-datacenter").
func (c *Client) Inventory(ctx context.Context) ([]VM, error) {
	root := c.Vim.ServiceContent.RootFolder
	sel := func(name string) *types.SelectionSpec { return &types.SelectionSpec{Name: name} }
	traversal := []types.BaseSelectionSpec{
		&types.TraversalSpec{SelectionSpec: types.SelectionSpec{Name: "folder"}, Type: "Folder", Path: "childEntity",
			SelectSet: []types.BaseSelectionSpec{sel("folder"), sel("datacenter")}},
		&types.TraversalSpec{SelectionSpec: types.SelectionSpec{Name: "datacenter"}, Type: "Datacenter", Path: "vmFolder",
			SelectSet: []types.BaseSelectionSpec{sel("folder")}},
	}
	req := types.RetrieveProperties{SpecSet: []types.PropertyFilterSpec{{
		ObjectSet: []types.ObjectSpec{{Obj: root, SelectSet: traversal}},
		PropSet: []types.PropertySpec{
			{Type: "Datacenter", PathSet: []string{"name"}},
			{Type: "VirtualMachine", PathSet: []string{"name", "runtime.powerState", "config", "layoutEx", "snapshot"}},
		},
	}}}
	res, err := property.DefaultCollector(c.Vim).RetrieveProperties(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("inventory: %w", err)
	}
	var dcs []mo.Datacenter
	var vms []mo.VirtualMachine
	var dcContent, vmContent []types.ObjectContent
	for _, oc := range res.Returnval {
		switch oc.Obj.Type {
		case "Datacenter":
			dcContent = append(dcContent, oc)
		case "VirtualMachine":
			vmContent = append(vmContent, oc)
		}
	}
	if err := mo.LoadObjectContent(dcContent, &dcs); err != nil {
		return nil, err
	}
	if err := mo.LoadObjectContent(vmContent, &vms); err != nil {
		return nil, err
	}
	if len(dcs) != 1 {
		return nil, fmt.Errorf("inventory: %d datacenters; stage 1 supports exactly one (a standalone ESXi host)", len(dcs))
	}
	var out []VM
	for _, m := range vms {
		out = append(out, toVM(m, dcs[0].Name))
	}
	slices.SortFunc(out, func(a, b VM) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func toVM(m mo.VirtualMachine, dc string) VM {
	v := VM{Name: m.Name, PowerState: string(m.Runtime.PowerState), Datacenter: dc, Snapshots: m.Snapshot != nil,
		ObjectRef: m.Reference().Value, Disks: []Disk{}, NICs: []NIC{}, Files: []File{}}
	if m.Config == nil {
		v.Unreadable = "no config (VM inaccessible or orphaned)"
		return v
	}
	cfg := m.Config
	v.UUID, v.GuestID, v.GuestName, v.Firmware, v.Template = cfg.Uuid, cfg.GuestId, cfg.GuestFullName, cfg.Firmware, cfg.Template
	v.CPUs, v.MemoryMiB, v.VMX = cfg.Hardware.NumCPU, cfg.Hardware.MemoryMB, cfg.Files.VmPathName
	if b := cfg.BootOptions; b != nil && b.EfiSecureBootEnabled != nil {
		v.SecureBoot = *b.EfiSecureBootEnabled
	}
	for _, d := range cfg.Hardware.Device {
		label := ""
		if di := d.GetVirtualDevice().DeviceInfo; di != nil {
			label = di.GetDescription().Label
		}
		switch dev := d.(type) {
		case *types.VirtualDisk:
			disk := Disk{Label: label, Capacity: dev.CapacityInBytes}
			if b, ok := dev.Backing.(*types.VirtualDiskFlatVer2BackingInfo); ok {
				disk.File = b.FileName
				disk.Thin = b.ThinProvisioned != nil && *b.ThinProvisioned
			} else if fb, ok := dev.Backing.(types.BaseVirtualDeviceFileBackingInfo); ok {
				disk.File = fb.GetVirtualDeviceFileBackingInfo().FileName
			}
			v.Disks = append(v.Disks, disk)
		case types.BaseVirtualEthernetCard:
			e := dev.GetVirtualEthernetCard()
			v.NICs = append(v.NICs, NIC{Label: label, Type: strings.TrimPrefix(fmt.Sprintf("%T", d), "*types."), MAC: e.MacAddress})
		}
	}
	if m.LayoutEx != nil {
		for _, f := range m.LayoutEx.File {
			v.Files = append(v.Files, File{Name: f.Name, Type: f.Type, Size: f.Size})
		}
	}
	return v
}

// ParseDatastorePath splits "[ds] dir/file" into ("ds", "dir/file").
func ParseDatastorePath(p string) (ds, rel string, err error) {
	if !strings.HasPrefix(p, "[") {
		return "", "", fmt.Errorf("not a datastore path: %q", p)
	}
	end := strings.Index(p, "]")
	if end < 0 {
		return "", "", fmt.Errorf("not a datastore path: %q", p)
	}
	return p[1:end], strings.TrimSpace(p[end+1:]), nil
}

// Downloaded describes one file read from a datastore.
type Downloaded struct {
	Name    string  `json:"name"` // datastore path
	Local   string  `json:"local"`
	Bytes   int64   `json:"bytes"`
	SHA256  string  `json:"sha256"`
	Seconds float64 `json:"seconds"`
}

// Download reads one datastore file with an HTTP GET on /folder and writes it to
// local, sparsely (runs of zeroes become holes), hashing it on the way.
func (c *Client) Download(ctx context.Context, datastorePath, datacenter, local string) (Downloaded, error) {
	start := time.Now()
	d := Downloaded{Name: datastorePath, Local: local}
	ds, rel, err := ParseDatastorePath(datastorePath)
	if err != nil {
		return d, err
	}
	u := &url.URL{Scheme: c.soap.URL().Scheme, Host: c.soap.URL().Host, Path: path.Join("/folder", rel),
		RawQuery: url.Values{"dcPath": {datacenter}, "dsName": {ds}}.Encode()}
	rc, _, err := c.soap.Download(ctx, u, &soap.DefaultDownload)
	if err != nil {
		return d, fmt.Errorf("GET %s: %w", datastorePath, err)
	}
	defer rc.Close()
	f, err := os.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return d, err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	zero := make([]byte, len(buf))
	for {
		n, rerr := io.ReadFull(rc, buf)
		if n > 0 {
			h.Write(buf[:n])
			if bytes.Equal(buf[:n], zero[:n]) {
				_, err = f.Seek(int64(n), io.SeekCurrent)
			} else {
				_, err = f.Write(buf[:n])
			}
			if err != nil {
				return d, err
			}
			d.Bytes += int64(n)
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return d, fmt.Errorf("GET %s: %w", datastorePath, rerr)
		}
	}
	if err := f.Truncate(d.Bytes); err != nil {
		return d, err
	}
	if err := f.Sync(); err != nil {
		return d, err
	}
	d.SHA256, d.Seconds = hex.EncodeToString(h.Sum(nil)), time.Since(start).Seconds()
	return d, f.Close()
}
