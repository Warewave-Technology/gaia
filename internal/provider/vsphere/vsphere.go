// Package vsphere is the part of vCenter Gaia talks to directly
// (design 4.2): power, snapshots and discovery with govmomi. VMs are
// created by Pulumi; a VM is named by its UUID (config.uuid), which Gaia
// records when it is created.
package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/Warewave-Technology/gaia/internal/compute"
)

// Credentials reach vCenter.
type Credentials struct {
	Endpoint string // https://vcenter (the SDK path is added)
	Username string
	Password string
	Insecure bool // self-signed certificate
}

// Connect logs in to vCenter.
func Connect(ctx context.Context, c Credentials) (*vim25.Client, error) {
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("vsphere: endpoint %q: want https://host", c.Endpoint)
	}
	if !strings.HasSuffix(u.Path, "/sdk") {
		u.Path += "/sdk"
	}
	u.User = url.UserPassword(c.Username, c.Password)
	gc, err := govmomi.NewClient(ctx, u, c.Insecure)
	if err != nil {
		return nil, fmt.Errorf("vsphere: %w", err)
	}
	return gc.Client, nil
}

// Compute is vCenter as a compute.Compute.
type Compute struct{ C *vim25.Client }

var _ compute.Compute = Compute{}

func (v Compute) vm(ctx context.Context, uuid string) (*object.VirtualMachine, error) {
	ref, err := object.NewSearchIndex(v.C).FindByUuid(ctx, nil, uuid, true, types.NewBool(false))
	if err != nil {
		return nil, fmt.Errorf("vsphere: %w", err)
	}
	if ref == nil {
		return nil, fmt.Errorf("vsphere: no VM %s", uuid)
	}
	return object.NewVirtualMachine(v.C, ref.Reference()), nil
}

// Power powers a VM on, or shuts it down or restarts it through VMware
// Tools; a shutdown is waited for (five minutes).
func (v Compute) Power(ctx context.Context, uuid, action string) error {
	vm, err := v.vm(ctx, uuid)
	if err != nil {
		return err
	}
	switch action {
	case compute.On:
		t, err := vm.PowerOn(ctx)
		if err != nil {
			return err
		}
		return t.Wait(ctx)
	case compute.Off:
		if err := vm.ShutdownGuest(ctx); err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		return vm.WaitForPowerState(wctx, types.VirtualMachinePowerStatePoweredOff)
	case compute.Restart:
		return vm.RebootGuest(ctx)
	}
	return compute.ErrUnknownAction
}

// Snapshot takes a snapshot without memory.
func (v Compute) Snapshot(ctx context.Context, uuid, name, description string) error {
	vm, err := v.vm(ctx, uuid)
	if err != nil {
		return err
	}
	t, err := vm.CreateSnapshot(ctx, name, description, false, false)
	if err != nil {
		return err
	}
	return t.Wait(ctx)
}

// Revert goes back to a snapshot and powers the VM on (a snapshot
// without memory leaves it off).
func (v Compute) Revert(ctx context.Context, uuid, name string) error {
	vm, err := v.vm(ctx, uuid)
	if err != nil {
		return err
	}
	t, err := vm.RevertToSnapshot(ctx, name, true)
	if err != nil {
		return err
	}
	if err := t.Wait(ctx); err != nil {
		return err
	}
	state, err := vm.PowerState(ctx)
	if err != nil || state == types.VirtualMachinePowerStatePoweredOn {
		return err
	}
	t, err = vm.PowerOn(ctx)
	if err != nil {
		return err
	}
	return t.Wait(ctx)
}

// DeleteSnapshot removes a snapshot, consolidating disks.
func (v Compute) DeleteSnapshot(ctx context.Context, uuid, name string) error {
	vm, err := v.vm(ctx, uuid)
	if err != nil {
		return err
	}
	t, err := vm.RemoveSnapshot(ctx, name, false, types.NewBool(true))
	if err != nil {
		return err
	}
	return t.Wait(ctx)
}

// Discover lists every VM and template vCenter has.
func (v Compute) Discover(ctx context.Context) ([]compute.ObservedVM, error) {
	m := view.NewManager(v.C)
	cv, err := m.CreateContainerView(ctx, v.C.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = cv.Destroy(context.WithoutCancel(ctx)) }()
	var vms []mo.VirtualMachine
	if err := cv.Retrieve(ctx, []string{"VirtualMachine"}, []string{"name", "config", "runtime.powerState", "guest.net"}, &vms); err != nil {
		return nil, err
	}
	out := make([]compute.ObservedVM, 0, len(vms))
	for _, x := range vms {
		o := compute.ObservedVM{Name: x.Name, PowerState: "other"}
		switch x.Runtime.PowerState {
		case types.VirtualMachinePowerStatePoweredOn:
			o.PowerState = compute.On
		case types.VirtualMachinePowerStatePoweredOff:
			o.PowerState = compute.Off
		}
		if c := x.Config; c != nil {
			o.ID, o.UUID, o.Template, o.Notes = c.Uuid, c.InstanceUuid, c.Template, c.Annotation
		}
		if x.Guest != nil {
			for _, n := range x.Guest.Net {
				o.IPs = append(o.IPs, n.IpAddress...)
			}
		}
		out = append(out, o)
	}
	return out, nil
}
