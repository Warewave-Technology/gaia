package cli

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/server"
)

func newPreviewCommand() *cobra.Command {
	var addr, repoName string
	var all bool
	cmd := &cobra.Command{
		Use:   "preview REF",
		Short: "Show what a branch, pull request or commit would change, planned by gaia server",
		Long: "preview asks gaia server to plan REF of a followed repository (a branch, refs/tags/…,\n" +
			"pull/N for a GitHub pull request, or a full commit SHA) with the other repositories\n" +
			"at their heads, and compares each contract with what is applied. Nothing is\n" +
			"applied and no change is opened. gaia plan --ref does the same from a local\n" +
			"repository, with access to Gaia's database.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var p server.Preview
			q := url.Values{"repo": {repoName}, "ref": {args[0]}}
			if err := apiClient(addr).Get(cmd.Context(), "/api/v1/preview?"+q.Encode(), &p); err != nil {
				return hint(err)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "%s %s → %s\n", p.Repo, p.Ref, short(p.Commit))
			if p.Error != "" {
				_, _ = fmt.Fprintf(out, "\n%s\n", p.Error)
				return fmt.Errorf("%s does not plan", p.Ref)
			}
			changed := 0
			for _, c := range p.Contracts {
				if !c.Changed && !all {
					continue
				}
				changed++
				switch {
				case c.Restricted:
					_, _ = fmt.Fprintf(out, "\n%s: changes (you see its name only)\n", c.Name)
				case c.Removed:
					_, _ = fmt.Fprintf(out, "\n%s: removed by %s\n", c.Name, p.Ref)
				default:
					state := "unchanged"
					if c.Changed {
						state = "changes"
					}
					_, _ = fmt.Fprintf(out, "\n%s: %s (plan %s)\n", c.Name, state, short(c.PlanHash))
					if c.Plan != nil && c.Changed {
						if err := plan.WriteText(out, &plan.Plan{Commit: p.Label, Contracts: []plan.Contract{*c.Plan}, Hash: c.PlanHash}); err != nil {
							return err
						}
					}
				}
			}
			if changed == 0 {
				_, _ = fmt.Fprintln(out, "\nno contract changes")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repoName, "repo", "platform", "the followed repository REF is in: platform, or a contract repository's provider")
	cmd.Flags().BoolVar(&all, "all", false, "list unchanged contracts too")
	serverFlag(cmd, &addr)
	return cmd
}
