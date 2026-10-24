package cli

import (
	"encoding/json"
	"io"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/plan"
)

func newPlanCommand() *cobra.Command {
	var src source
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "plan [contract...]",
		Short: "Show what applying contracts at a commit would do",
		Long: "plan resolves the catalog and contracts at --ref and shows, per Server contract,\n" +
			"the servers to create and what runs on them. It changes nothing. With no\n" +
			"contract names every Server contract is planned.",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			// With --json, only the plan goes to stdout.
			info := out
			if asJSON {
				info = io.Discard
			}
			g, err := src.graph(info)
			if err != nil {
				return err
			}
			p, err := plan.Build(g, args)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(p)
			}
			return plan.WriteText(out, p)
		},
	}
	src.flags(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the plan as JSON")
	return cmd
}
