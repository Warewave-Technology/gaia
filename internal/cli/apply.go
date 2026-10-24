package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/engine/ansible"
	"github.com/Warewave-Technology/gaia/internal/engine/pulumi"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/provider/openbao"
	"github.com/Warewave-Technology/gaia/internal/provider/proxmox"
	"github.com/Warewave-Technology/gaia/internal/provider/zabbix"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func newApplyCommand() *cobra.Command {
	var src source
	var dsn string
	var yes bool
	cmd := &cobra.Command{
		Use:   "apply [contract...]",
		Short: "Apply contracts at a commit",
		Long: "apply plans like `gaia plan`, prints the plan and, with --yes, carries it out:\n" +
			"IPAM addresses, VMs through Pulumi, registration in monitoring, the baseline and\n" +
			"roles through ansible-runner in the execution environment (podman), and the\n" +
			"records in Gaia's database.\n" +
			"OpenBao is reached at the Platform's secrets provider with $GAIA_BAO_TOKEN.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
			}
			token := os.Getenv("GAIA_BAO_TOKEN")
			if token == "" {
				return errors.New("no OpenBao token: set GAIA_BAO_TOKEN")
			}
			g, err := src.graph(out)
			if err != nil {
				return err
			}
			s, err := store.Open(ctx, d)
			if err != nil {
				return err
			}
			defer s.Close()
			p, err := plan.Build(ctx, g, args, apply.StoreState{S: s})
			if err != nil {
				return err
			}
			if err := plan.WriteText(out, p); err != nil {
				return err
			}
			if !yes {
				_, err := fmt.Fprintln(out, "\nnothing applied: run again with --yes to apply this plan")
				return err
			}

			bao := openbao.New(secretsEndpoint(g), token)
			origin, err := src.recordSource(p.Commit)
			if err != nil {
				return err
			}
			run, err := apply.Run(ctx, g, p, apply.Deps{
				Store:     s,
				Secrets:   bao,
				Provision: pulumi.Up,
				UsedVMIDs: func(ctx context.Context, c proxmox.Credentials) (map[int]bool, error) {
					return proxmox.New(c).UsedVMIDs(ctx)
				},
				Configure: ansible.Podman{},
				Monitor:   func(endpoint, token string) apply.Monitor { return zabbix.New(endpoint, token) },
				Archive: func(endpoint, bucket, ak, sk string) (runrecord.Archive, error) {
					return runrecord.NewS3(endpoint, bucket, ak, sk)
				},
				RequestedBy: requester(),
				Source:      origin,
				Out:         out,
			})
			if err != nil {
				return fmt.Errorf("run %s: %w", run, err)
			}
			_, err = fmt.Fprintf(out, "\nrun %s succeeded\n", run)
			return err
		},
	}
	src.flags(cmd)
	dsnFlag(cmd, &dsn)
	cmd.Flags().BoolVar(&yes, "yes", false, "apply the plan; without it apply only prints it")
	return cmd
}

// requester is who runs the CLI, for the run record. In the PoC that is
// the local account; with the platform service it is the PR author.
func requester() string {
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	host, _ := os.Hostname()
	return name + "@" + host
}

// recordSource is the run record's source: the repository and whether
// the commit is signed.
func (s *source) recordSource(commit string) (runrecord.Source, error) {
	repo, err := filepath.Abs(s.repo)
	if err != nil {
		return runrecord.Source{}, err
	}
	signed, err := gitsrc.Signed(repo, commit)
	if err != nil {
		return runrecord.Source{}, err
	}
	return runrecord.Source{Repo: repo, Commit: commit, Signed: signed}, nil
}

func secretsEndpoint(g *catalog.Graph) string {
	name := g.Platform().Spec.(*catalog.PlatformSpec).Secrets
	o, _ := g.Catalog.Get(catalog.KindProvider, name)
	return o.Spec.(*catalog.ProviderSpec).Endpoint
}
