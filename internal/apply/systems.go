package apply

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/Warewave-Technology/gaia/internal/compute"
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
		Compute:     Compute,
		Configure:   ansible.Podman{},
		Monitor:     func(endpoint, token string) Monitor { return zabbix.New(endpoint, token) },
		Archive: func(endpoint, bucket, ak, sk string) (runrecord.Archive, error) {
			return runrecord.NewS3(endpoint, bucket, ak, sk)
		},
		Out: out,
	}
}

// Compute opens a hypervisor's day-2 API.
func Compute(kind, endpoint string, secret map[string]string) (compute.Compute, error) {
	switch kind {
	case "proxmox":
		insecure, _ := strconv.ParseBool(secret["insecure"])
		return proxmox.Compute{C: proxmox.New(proxmox.Credentials{Endpoint: endpoint, TokenID: secret["token_id"], TokenSecret: secret["token_secret"], Insecure: insecure})}, nil
	}
	return nil, fmt.Errorf("no day-2 API for %s", kind)
}
