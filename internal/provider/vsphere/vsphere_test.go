package vsphere

import (
	"context"
	"errors"
	"testing"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/Warewave-Technology/gaia/internal/compute"
)

func TestDayTwoOnVcsim(t *testing.T) {
	simulator.Test(func(ctx context.Context, c *vim25.Client) {
		f := find.NewFinder(c)
		vm, err := f.VirtualMachine(ctx, "DC0_H0_VM0")
		if err != nil {
			t.Fatal(err)
		}
		var o mo.VirtualMachine
		if err := vm.Properties(ctx, vm.Reference(), []string{"config.uuid"}, &o); err != nil {
			t.Fatal(err)
		}
		uuid := o.Config.Uuid
		v := Compute{C: c}

		if err := v.Snapshot(ctx, uuid, "gaia_before", "Gaia"); err != nil {
			t.Fatal(err)
		}
		if err := v.Power(ctx, uuid, compute.Off); err != nil {
			t.Fatal(err)
		}
		if st, _ := vm.PowerState(ctx); st != types.VirtualMachinePowerStatePoweredOff {
			t.Errorf("after off: %s", st)
		}
		if err := v.Power(ctx, uuid, compute.On); err != nil {
			t.Fatal(err)
		}
		if err := v.Revert(ctx, uuid, "gaia_before"); err != nil {
			t.Fatal(err)
		}
		if st, _ := vm.PowerState(ctx); st != types.VirtualMachinePowerStatePoweredOn {
			t.Errorf("after revert: %s", st)
		}
		if err := v.DeleteSnapshot(ctx, uuid, "gaia_before"); err != nil {
			t.Fatal(err)
		}
		if err := v.Snapshot(ctx, "00000000-0000-0000-0000-000000000000", "x", ""); err == nil {
			t.Error("a VM that is not there")
		}
		if err := v.Power(ctx, uuid, "pause"); err == nil {
			t.Error("an unknown action")
		}
		vms, err := v.Discover(ctx)
		if err != nil || len(vms) < 2 {
			t.Fatalf("discover = %d, %v", len(vms), err)
		}
		found := false
		for _, x := range vms {
			found = found || x.ID == uuid && x.Name == "DC0_H0_VM0" && x.PowerState == compute.On
		}
		if !found {
			t.Errorf("DC0_H0_VM0 not in %+v", vms)
		}
		if err := v.Delete(ctx, uuid); err != nil {
			t.Fatal(err)
		}
		if err := v.Delete(ctx, uuid); !errors.Is(err, compute.ErrNoVM) {
			t.Errorf("deleted twice: %v", err)
		}
	})
}
