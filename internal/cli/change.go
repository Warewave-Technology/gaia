package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func newChangeCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "change", Short: "List and inspect changes (design 7.6)"}
	var addr, contract string
	var all bool

	list := &cobra.Command{
		Use: "list", Short: "List open changes (--all for every change)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out []map[string]any
			path := "/api/v1/changes?limit=50"
			if !all {
				path += "&open=true"
			}
			if contract != "" {
				path += "&contract=" + contract
			}
			if err := apiClient(addr).Get(cmd.Context(), path, &out); err != nil {
				return hint(err)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "CHANGE\tCONTRACT\tSTATUS\tBY\tCREATED")
			for _, c := range out {
				created, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(c["createdAt"]))
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c["id"], c["contract"], c["status"], joinAny(c["requestedBy"]), created.Local().Format(time.DateTime))
			}
			return tw.Flush()
		},
	}
	list.Flags().StringVar(&contract, "contract", "", "only changes of this contract")
	list.Flags().BoolVar(&all, "all", false, "every change, not only open ones")

	show := &cobra.Command{
		Use: "show CHANGE", Short: "Show a change: what it needs and who signed it", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var c struct {
				ID, Contract, Status, Label, PlanHash, RunID string
				RequestedBy                                  []string
				Gates                                        []struct {
					Gate, Status string
					Detail       json.RawMessage
				}
				Approvals []struct{ Username, Decision string }
				Commits   []struct{ Repo, Commit, Signer, Problem string }
			}
			if err := apiClient(addr).Get(cmd.Context(), "/api/v1/changes/"+args[0], &c); err != nil {
				return hint(err)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "change   %s (%s)\nstatus   %s\nplan     %s\nby       %s\n", c.ID, c.Contract, c.Status, c.PlanHash, strings.Join(c.RequestedBy, ", "))
			if c.RunID != "" {
				_, _ = fmt.Fprintf(out, "run      %s\n", c.RunID)
			}
			_, _ = fmt.Fprintln(out, "\nneeds")
			for _, g := range c.Gates {
				_, _ = fmt.Fprintf(out, "  %-10s %s  %s\n", g.Gate, g.Status, gateDetail(g.Gate, g.Detail))
			}
			if len(c.Commits) > 0 {
				_, _ = fmt.Fprintln(out, "\ncommits")
				for _, cm := range c.Commits {
					who := cm.Signer
					if cm.Problem != "" {
						who = "! " + cm.Problem
					}
					_, _ = fmt.Fprintf(out, "  %s %s  %s\n", cm.Repo, short(cm.Commit), who)
				}
			}
			for _, a := range c.Approvals {
				_, _ = fmt.Fprintf(out, "  %s: %s\n", a.Username, a.Decision)
			}
			return nil
		},
	}

	retry := &cobra.Command{
		Use: "retry CHANGE", Short: "Try a failed or cancelled change again (operator)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out map[string]string
			if err := apiClient(addr).Post(cmd.Context(), "/api/v1/changes/"+args[0]+"/retry", nil, &out); err != nil {
				return hint(err)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "opened %s\n", out["change"])
			return err
		},
	}

	for _, c := range []*cobra.Command{list, show, retry} {
		serverFlag(c, &addr)
		cmd.AddCommand(c)
	}
	return cmd
}

// gateDetail says in a line what a gate waits for.
func gateDetail(gate string, raw json.RawMessage) string {
	if gate != "approval" {
		return ""
	}
	var need struct {
		Groups []struct {
			Name       string
			Candidates []string
			ApprovedBy string
		}
		Excluded []string
	}
	_ = json.Unmarshal(raw, &need)
	var parts []string
	for _, g := range need.Groups {
		if g.ApprovedBy != "" {
			parts = append(parts, g.Name+": "+g.ApprovedBy)
		} else {
			parts = append(parts, g.Name+": one of "+strings.Join(g.Candidates, ", "))
		}
	}
	if len(parts) == 0 {
		return "not needed"
	}
	return strings.Join(parts, "; ")
}
