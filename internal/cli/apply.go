package cli

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/catalog"
	"github.com/Warewave-Technology/gaia/internal/gitsrc"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/provider/openbao"
	"github.com/Warewave-Technology/gaia/internal/queue"
	"github.com/Warewave-Technology/gaia/internal/repo"
	"github.com/Warewave-Technology/gaia/internal/runrecord"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func newApplyCommand() *cobra.Command {
	var src source
	var dsn string
	var yes, queued bool
	cmd := &cobra.Command{
		Use:   "apply [contract...]",
		Short: "Apply contracts at a commit",
		Long: "apply plans like `gaia plan`, prints the plan and, with --yes, carries it out:\n" +
			"IPAM addresses, VMs through Pulumi, registration in monitoring, the baseline and\n" +
			"roles through ansible-runner in the execution environment (podman), and the\n" +
			"records in Gaia's database.\n" +
			"OpenBao is reached at the Platform's secrets provider with $GAIA_BAO_TOKEN.\n" +
			"With --queue the runs go to gaia server's workers instead.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			d, err := resolveDSN(dsn)
			if err != nil {
				return err
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
			if queued {
				q, err := river.NewClient(riverpgxv5.New(s.Pool()), &river.Config{})
				if err != nil {
					return err
				}
				// The repository's top: workers read it as a local repository.
				dir, err := gitsrc.Root(src.repo)
				if err != nil {
					return err
				}
				snap := repo.Snapshot{Platform: repo.Rev{Name: "platform", URL: dir, Commit: p.Commit, Paths: src.paths}}
				runs, err := queue.Enqueue(ctx, s, q, g, snap, p.ContractNames(), requester())
				if err != nil {
					return err
				}
				for i, run := range runs {
					if _, err := fmt.Fprintf(out, "\nqueued %s (%s)", run, p.ContractNames()[i]); err != nil {
						return err
					}
				}
				_, err = fmt.Fprintln(out)
				return err
			}

			token := os.Getenv("GAIA_BAO_TOKEN")
			if token == "" {
				return errors.New("no OpenBao token: set GAIA_BAO_TOKEN")
			}
			bao := openbao.New(secretsEndpoint(g), token)
			origin, err := src.recordSource(p.Commit)
			if err != nil {
				return err
			}
			deps := apply.Systems(s, bao, out)
			deps.RequestedBy, deps.Source = requester(), origin
			run, err := apply.Run(ctx, g, p, deps)
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
	cmd.Flags().BoolVar(&queued, "queue", false, "with --yes, queue one run per contract for gaia server's workers instead of running here")
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
