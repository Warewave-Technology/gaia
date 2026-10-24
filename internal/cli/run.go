package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Warewave-Technology/gaia/internal/client"
	"github.com/Warewave-Technology/gaia/internal/server"
)

func serverFlag(cmd *cobra.Command, s *string) {
	def := os.Getenv("GAIA_SERVER")
	if def == "" {
		def = "http://127.0.0.1:8080"
	}
	cmd.Flags().StringVar(s, "server", def, "gaia server's address ($GAIA_SERVER)")
}

func newRunCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "run", Short: "List, follow and cancel runs on gaia server"}
	var addr, contract string
	var limit int

	list := &cobra.Command{
		Use: "list", Short: "List runs, newest first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runs, err := client.New(addr).Runs(cmd.Context(), contract, limit)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "RUN\tCONTRACTS\tSTATUS\tCOMMIT\tQUEUED\tBY")
			for _, r := range runs {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, strings.Join(r.Contracts, ","), r.Status, short(r.Commit), r.QueuedAt.Local().Format(time.DateTime), r.RequestedBy)
			}
			return tw.Flush()
		},
	}
	list.Flags().StringVar(&contract, "contract", "", "only runs of this contract")
	list.Flags().IntVar(&limit, "limit", 20, "how many")

	show := &cobra.Command{
		Use: "show RUN", Short: "Show a run's stages and record", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := client.New(addr).Run(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "run      %s\nstatus   %s\ncommit   %s\nplan     %s\nby       %s\n", r.ID, r.Status, r.Commit, r.PlanHash, r.RequestedBy)
			if r.Error != "" {
				_, _ = fmt.Fprintf(out, "error    %s\n", r.Error)
			}
			for _, st := range r.Stages {
				name := st.Stage
				if st.Contract != "" {
					name = st.Contract + "/" + st.Stage
				}
				_, _ = fmt.Fprintf(out, "  %-28s %s", name, st.Status)
				if st.FinishedAt != nil {
					_, _ = fmt.Fprintf(out, " (%s)", st.FinishedAt.Sub(st.StartedAt).Round(time.Second))
				}
				_, _ = fmt.Fprintln(out)
			}
			if r.Record != nil {
				_, _ = fmt.Fprintf(out, "record   %d %s\n", r.Record.Seq, r.Record.RecordHash)
			}
			return nil
		},
	}

	watch := &cobra.Command{
		Use: "watch RUN", Short: "Follow a run's output until it ends", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			status, err := client.New(addr).Watch(cmd.Context(), args[0], 0, func(e server.Event) error {
				return printEvent(out, e)
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "\nrun %s %s\n", args[0], status)
			if err == nil && status != "succeeded" {
				err = fmt.Errorf("run %s %s", args[0], status)
			}
			return err
		},
	}

	cancel := &cobra.Command{
		Use: "cancel RUN", Short: "Cancel a queued or running run", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.New(addr).Cancel(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "cancelling %s\n", args[0])
			return err
		},
	}

	for _, c := range []*cobra.Command{list, show, watch, cancel} {
		serverFlag(c, &addr)
		cmd.AddCommand(c)
	}
	return cmd
}

// printEvent prints output lines as they are and Ansible events as one
// line per task result.
func printEvent(out interface{ Write([]byte) (int, error) }, e server.Event) error {
	if e.Type == "log" {
		_, err := fmt.Fprintln(out, e.Stdout)
		return err
	}
	if e.Host == "" || e.Task == "" || e.Type == "runner_on_start" {
		return nil
	}
	state := "ok"
	switch {
	case e.Failed:
		state = "failed"
	case e.Changed:
		state = "changed"
	}
	_, err := fmt.Fprintf(out, "    %-8s %s  %s\n", state, e.Host, e.Task)
	return err
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
