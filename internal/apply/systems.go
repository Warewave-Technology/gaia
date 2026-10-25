package apply

import (
	"context"
	"io"

	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/provider/zabbix"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
)

// Systems are the real systems a run talks to: Pulumi, the Proxmox API,
// ansible-runner in podman, Zabbix and the S3 archive. The CLI and the
// queue's workers both use them.
func Systems(s *store.Store, secrets Secrets, out io.Writer) Deps {
	return Deps{
		Store:     s,
		Secrets:   secrets,
		Provision: pulumi.Up,
		UsedVMIDs: func(ctx context.Context, c proxmox.Credentials) (map[int]bool, error) {
			return proxmox.New(c).UsedVMIDs(ctx)
		},
		ScanHostKey: ScanHostKey,
		Snapshot: func(ctx context.Context, c proxmox.Credentials, vmid int, name, description string) error {
			return proxmox.New(c).Snapshot(ctx, vmid, name, description)
		},
		Configure: ansible.Podman{},
		Monitor:   func(endpoint, token string) Monitor { return zabbix.New(endpoint, token) },
		Archive: func(endpoint, bucket, ak, sk string) (runrecord.Archive, error) {
			return runrecord.NewS3(endpoint, bucket, ak, sk)
		},
		Out: out,
	}
}
