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

func newReconcileCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "reconcile CONTRACT",
		Short: "Apply a contract's applied plan again, putting drift right; its owner approves (operator)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Change string `json:"change"`
			}
			if err := apiClient(addr).Post(cmd.Context(), "/api/v1/contracts/"+url.PathEscape(args[0])+"/reconcile", nil, &out); err != nil {
				return hint(err)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "change %s\n", out.Change)
			return nil
		},
	}
	serverFlag(cmd, &addr)
	return cmd
}

func newDiscoverCommand() *cobra.Command {
	var addr string
	var now bool
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Show the latest discovery report: VMs and monitoring hosts next to Gaia's records (operator)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := apiClient(addr)
			if now {
				if err := c.Post(cmd.Context(), "/api/v1/discovery", nil, nil); err != nil {
					return hint(err)
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "requested; the report comes within a minute or two")
				return nil
			}
			var rep struct {
				TakenAt time.Time         `json:"takenAt"`
				Counts  map[string]int    `json:"counts"`
				Errors  map[string]string `json:"errors"`
				VMs     []map[string]any  `json:"vms"`
				Missing []map[string]any  `json:"missing"`
				Hosts   []map[string]any  `json:"hosts"`
			}
			if err := c.Get(cmd.Context(), "/api/v1/discovery", &rep); err != nil {
				return hint(err)
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "taken %s\n", rep.TakenAt.Local().Format(time.DateTime))
			for p, e := range rep.Errors {
				_, _ = fmt.Fprintf(w, "%s: %s\n", p, e)
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "CATEGORY\tPROVIDER\tVM\tNAME\tPOWER\tSERVER")
			for _, v := range rep.VMs {
				if v["category"] == "managed" || v["category"] == "template" {
					continue
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", v["category"], v["provider"], v["id"], v["name"], v["powerState"], str(v["uid"]))
			}
			for _, m := range rep.Missing {
				_, _ = fmt.Fprintf(tw, "missing\t%s\t%s\t\t\t%s (%s)\n", m["provider"], m["id"], m["hostname"], m["contract"])
			}
			for _, h := range rep.Hosts {
				_, _ = fmt.Fprintf(tw, "monitoring orphan\t%s\t\t%s\t\t%s\n", h["provider"], h["name"], h["uid"])
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(w, "counts: %v\n", rep.Counts)
			return nil
		},
	}
	cmd.Flags().BoolVar(&now, "now", false, "ask for a report now")
	serverFlag(cmd, &addr)
	return cmd
}
