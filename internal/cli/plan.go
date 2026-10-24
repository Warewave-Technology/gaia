package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/apply"
	"github.com/Warewave-Technology/gaia/internal/plan"
	"github.com/Warewave-Technology/gaia/internal/store"
)

func newPlanCommand() *cobra.Command {
	var src source
	var asJSON bool
	var dsn string
	cmd := &cobra.Command{
		Use:   "plan [contract...]",
		Short: "Show what applying contracts at a commit would do",
		Long: "plan resolves the catalog and contracts at --ref and shows, per Server contract,\n" +
			"which servers are created, kept or deleted and what runs on them. It changes\n" +
			"nothing. Existing servers are read from Gaia's database (--dsn or $GAIA_DSN);\n" +
			"without one every server is new. With no contract names every Server\n" +
			"contract is planned.",
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
			st, closeState, err := planState(cmd, dsn, info)
			if err != nil {
				return err
			}
			defer closeState()
			p, err := plan.Build(cmd.Context(), g, args, st)
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
	dsnFlag(cmd, &dsn)
	return cmd
}

// planState opens Gaia's database when one is given; without one nothing
// exists yet, which info says.
func planState(cmd *cobra.Command, dsn string, info io.Writer) (plan.State, func(), error) {
	d, err := resolveDSN(dsn)
	if err != nil {
		_, werr := fmt.Fprintln(info, "no database (--dsn or GAIA_DSN): every server is planned as new")
		return plan.NoState{}, func() {}, werr
	}
	s, err := store.Open(cmd.Context(), d)
	if err != nil {
		return nil, nil, err
	}
	return apply.StoreState{S: s}, s.Close, nil
}
