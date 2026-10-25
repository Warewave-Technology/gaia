// Package compute is what Gaia does to VMs besides creating and deleting
// them (design 4.2): power, snapshots, discovery. Every hypervisor
// implements Compute, so that Operations and discovery do not care which
// one a server is on.
package compute

import (
	"context"
	"errors"
)

// Power actions.
const (
	On      = "on"
	Off     = "off"     // graceful, through the guest agent / VMware Tools
	Restart = "restart" // graceful
)

// Compute is one hypervisor's API. A VM is named by the ID Gaia recorded
// for it: the VMID on Proxmox, the instance UUID on vSphere.
type Compute interface {
	Power(ctx context.Context, vm, action string) error
	Snapshot(ctx context.Context, vm, name, description string) error
	Revert(ctx context.Context, vm, name string) error
	DeleteSnapshot(ctx context.Context, vm, name string) error
	Discover(ctx context.Context) ([]ObservedVM, error)
}

// ObservedVM is a VM as its hypervisor reports it.
type ObservedVM struct {
	ID         string   `json:"id"`   // what Gaia records: VMID or instance UUID
	Name       string   `json:"name"` // as the hypervisor names it
	UUID       string   `json:"uuid,omitempty"`
	PowerState string   `json:"powerState"` // on | off | other
	Tags       []string `json:"tags,omitempty"`
	Notes      string   `json:"notes,omitempty"`
	IPs        []string `json:"ips,omitempty"`
	Template   bool     `json:"template,omitempty"`
}

// ErrUnknownAction: not a power action.
var ErrUnknownAction = errors.New("power: on, off or restart")
