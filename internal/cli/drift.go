package cli

import (
	"fmt"
	"net/url"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func newDriftCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "drift [contract]",
		Short: "Show where the systems are not what was applied (design 7.5)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/v1/drift"
			if len(args) == 1 {
				path += "?contract=" + url.QueryEscape(args[0])
			}
			var out []map[string]any
			if err := apiClient(addr).Get(cmd.Context(), path, &out); err != nil {
				return hint(err)
			}
			if len(out) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no drift")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "CONTRACT\tSERVER\tAREA\tWHAT\tWANT\tGOT\tSINCE\tCHANGE")
			for _, d := range out {
				since, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(d["firstSeen"]))
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d["contract"], str(d["hostname"]), str(d["area"]), str(d["key"]),
					str(d["want"]), str(d["got"]), since.Local().Format(time.DateTime), str(d["change"]))
			}
			return tw.Flush()
		},
	}
	serverFlag(cmd, &addr)
	return cmd
}

// str prints a JSON value, nothing for none.
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
