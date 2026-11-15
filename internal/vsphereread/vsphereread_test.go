package vsphereread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// esx starts vcsim as a standalone ESXi host and returns its URL (with credentials).
func esx(t *testing.T) *url.URL {
	t.Helper()
	m := simulator.ESX()
	if err := m.Create(); err != nil {
		t.Fatal(err)
	}
	s := m.Service.NewServer()
	t.Cleanup(func() { s.Close(); m.Remove() })
	return s.URL
}

func connect(t *testing.T, u *url.URL, log *bytes.Buffer) *Client {
	t.Helper()
	pw, _ := u.User.Password()
	bare := *u
	bare.User = nil
	var w io.Writer
	if log != nil {
		w = log
	}
	c, err := Connect(context.Background(), bare.String(), u.User.Username(), pw, true, w)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Logout(context.Background()) })
	return c
}

// unguarded is a plain govmomi client, to look at the simulator's state from the outside.
func unguarded(t *testing.T, u *url.URL) *govmomi.Client {
	t.Helper()
	c, err := govmomi.NewClient(context.Background(), u, true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSOAPOperation(t *testing.T) {
	for body, want := range map[string]string{
		`<?xml version="1.0"?><soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"><soapenv:Body><RetrievePropertiesEx xmlns="urn:vim25"><_this>x</_this></RetrievePropertiesEx></soapenv:Body></soapenv:Envelope>`: "RetrievePropertiesEx",
		`<Envelope><Header><Login/></Header><Body><PowerOnVM_Task/></Body></Envelope>`: "PowerOnVM_Task",
		`<Envelope><Body></Body></Envelope>`:                                           "",
		`not xml`:                                                                      "",
		``:                                                                             "",
	} {
		if got := SOAPOperation([]byte(body)); got != want {
			t.Errorf("%q: %q, want %q", body, got, want)
		}
	}
}

func TestInventoryThroughGuard(t *testing.T) {
	u := esx(t)
	var log bytes.Buffer
	c := connect(t, u, &log)
	vms, err := c.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) == 0 {
		t.Fatal("no VMs")
	}
	v := vms[0]
	if v.Name == "" || v.PowerState == "" || v.VMX == "" || v.Datacenter != "ha-datacenter" || len(v.Disks) == 0 || len(v.NICs) == 0 || v.NICs[0].MAC == "" {
		t.Fatalf("inventory entry %+v", v)
	}
	for _, call := range c.Guard.Calls() {
		if !call.Allowed || (call.Op != "" && !ReadOps[call.Op]) {
			t.Fatalf("unexpected call %+v", call)
		}
	}
	if !strings.Contains(log.String(), `"op":"RetrievePropertiesEx"`) || !strings.Contains(log.String(), `"op":"Login"`) {
		t.Fatalf("call log:\n%s", log.String())
	}
}

// Every write the migration check must never do is refused before it reaches the host,
// and the host's state shows it: the VM is still off, has no snapshot, still exists.
func TestWritesAreBlocked(t *testing.T) {
	u := esx(t)
	ctx := context.Background()
	var log bytes.Buffer
	c := connect(t, u, &log)
	vms, err := c.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var target VM
	for _, v := range vms {
		if v.PowerState == string(types.VirtualMachinePowerStatePoweredOn) {
			target = v
		}
	}
	if target.Name == "" {
		t.Fatal("simulator has no powered-on VM")
	}
	vm := object.NewVirtualMachine(c.Vim, types.ManagedObjectReference{Type: "VirtualMachine", Value: target.ObjectRef})
	attempts := map[string]func() error{
		"PowerOffVM_Task":     func() error { _, err := vm.PowerOff(ctx); return err },
		"CreateSnapshot_Task": func() error { _, err := vm.CreateSnapshot(ctx, "x", "", false, false); return err },
		"ReconfigVM_Task": func() error {
			_, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{ChangeTrackingEnabled: types.NewBool(true)})
			return err
		},
		"Destroy_Task": func() error { _, err := vm.Destroy(ctx); return err },
		"UnregisterVM": func() error { return vm.Unregister(ctx) },
		"ExportVm":     func() error { _, err := vm.Export(ctx); return err },
		"CreateContainerView": func() error {
			_, err := view.NewManager(c.Vim).CreateContainerView(ctx, c.Vim.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
			return err
		},
	}
	for op, f := range attempts {
		err := f()
		if !errors.Is(err, ErrBlocked) && !strings.Contains(errString(err), ErrBlocked.Error()) {
			t.Errorf("%s: error %v, want blocked", op, err)
		}
	}
	ds, rel, _ := ParseDatastorePath(target.VMX)
	upload := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/folder/" + filepath.Dir(rel) + "/x.txt",
		RawQuery: url.Values{"dcPath": {"ha-datacenter"}, "dsName": {ds}}.Encode()}
	if err := c.soap.Upload(ctx, strings.NewReader("x"), upload, &soap.DefaultUpload); err == nil || !strings.Contains(err.Error(), ErrBlocked.Error()) {
		t.Errorf("datastore PUT: %v, want blocked", err)
	}

	blocked := 0
	for _, call := range c.Guard.Calls() {
		if !call.Allowed {
			blocked++
			if call.Status != 0 {
				t.Errorf("blocked call reached the host: %+v", call)
			}
		}
	}
	if blocked != len(attempts)+1 {
		t.Errorf("%d blocked calls logged, want %d:\n%s", blocked, len(attempts)+1, log.String())
	}

	// The host's view, through a client without the guard.
	plain := unguarded(t, u)
	var m mo.VirtualMachine
	ref := types.ManagedObjectReference{Type: "VirtualMachine", Value: target.ObjectRef}
	if err := plain.RetrieveOne(ctx, ref, []string{"runtime.powerState", "snapshot", "config.changeTrackingEnabled"}, &m); err != nil {
		t.Fatalf("VM gone? %v", err)
	}
	if m.Runtime.PowerState != types.VirtualMachinePowerStatePoweredOn || m.Snapshot != nil {
		t.Fatalf("host state changed: power %s, snapshot %v", m.Runtime.PowerState, m.Snapshot)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestDownload(t *testing.T) {
	u := esx(t)
	ctx := context.Background()
	c := connect(t, u, nil)
	vms, err := c.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Put a file next to the first VM's vmx, through the simulator's own datastore directory.
	plain := unguarded(t, u)
	f := find.NewFinder(plain.Client, true)
	dc, _ := f.DefaultDatacenter(ctx)
	f.SetDatacenter(dc)
	ds, rel, _ := ParseDatastorePath(vms[0].VMX)
	dso, err := f.Datastore(ctx, ds)
	if err != nil {
		t.Fatal(err)
	}
	var dsm mo.Datastore
	if err := dso.Properties(ctx, dso.Reference(), []string{"info"}, &dsm); err != nil {
		t.Fatal(err)
	}
	dir := strings.TrimPrefix(dsm.Info.GetDatastoreInfo().Url, "file://")
	data := make([]byte, 5<<20)
	copy(data[3<<20:], bytes.Repeat([]byte("gaia"), 1000)) // zeroes, then data: the start becomes a hole
	name := filepath.Join(filepath.Dir(rel), "disk-flat.vmdk")
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "disk-flat.vmdk")
	d, err := c.Download(ctx, "["+ds+"] "+name, "ha-datacenter", local)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	got, _ := os.ReadFile(local)
	if d.Bytes != int64(len(data)) || d.SHA256 != hex.EncodeToString(sum[:]) || !bytes.Equal(got, data) {
		t.Fatalf("download %+v", d)
	}
	if runtime.GOOS == "linux" {
		st, _ := os.Stat(local)
		if alloc := st.Sys().(*syscall.Stat_t).Blocks * 512; alloc > 3<<20 {
			t.Fatalf("download allocates %d bytes, the zero start should be a hole", alloc)
		}
	}
	if _, err := c.Download(ctx, "["+ds+"] nope/missing.vmdk", "ha-datacenter", filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("missing file downloaded")
	}
	for _, call := range c.Guard.Calls() {
		if !call.Allowed {
			t.Fatalf("download made a blocked call: %+v", call)
		}
	}
}

func TestParseDatastorePath(t *testing.T) {
	ds, rel, err := ParseDatastorePath("[datastore 1] vm one/vm one.vmx")
	if err != nil || ds != "datastore 1" || rel != "vm one/vm one.vmx" {
		t.Fatalf("%q %q %v", ds, rel, err)
	}
	if _, _, err := ParseDatastorePath("/vmfs/volumes/x/y.vmx"); err == nil {
		t.Fatal("accepted a non-datastore path")
	}
}
